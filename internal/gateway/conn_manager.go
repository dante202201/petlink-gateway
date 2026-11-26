// Package gateway 实现 WebSocket 接入网关：连接管理、鉴权、心跳、消息解析与下行下发。
// 本层只处理连接与转发，不包含业务逻辑。
package gateway

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"pet-translate-gateway/config"
	"pet-translate-gateway/internal/api"
	kafkax "pet-translate-gateway/internal/kafka"
	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/pipeline"
	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/store"
)

// OutboundMsg 写协程统一出站消息（文本指令或二进制 TTS）。
type OutboundMsg struct {
	MsgType int // websocket.TextMessage / BinaryMessage
	Data    []byte
}

// DeviceConn 单设备连接上下文，读写分离。
type DeviceConn struct {
	SN       string
	ConnID   string
	Conn     *websocket.Conn
	SendCh   chan OutboundMsg
	CloseCh  chan struct{}
	LastPong time.Time
	// clientSeen 是否已收到设备侧任意帧（Ping/Pong/业务）；用于避免上线立即对端发 Ping 弄挂部分固件栈
	clientSeen  bool
	ConnectedAt time.Time
	mu          sync.Mutex
	writeMu     sync.Mutex
	once        sync.Once

	// persistCh 音频入库/Kafka 与读循环解耦：读协程只做 CRC/去重后入队。
	// 避免 MySQL/Kafka 卡住 ReadMessage → TCP 反压 → 设备编码任务里 WS 发送超 32ms 丢 PCM。
	persistCh      chan audioPersistJob
	lastRedisTouch time.Time

	// 双帧协议状态：JSON 元数据后等待 Binary OPUS。
	// 固件 sendAudioChunk/flushBatch 已加发送互斥，Text+Binary 成对不会跨 kind 交错。
	pendingMeta *model.DeviceUplink
	batchRemain int // batch 模式下剩余预期分片数（仅校验/日志）

	// 按 kind 分桶的段状态（事件流 / 录音文件绝不能共用基准 ts 与补发缓存）
	kindAudio [2]audioKindConnState

	// a_emb_sn 绑定缓存：bind_user_id/bind_pet_id 为空或 0 时拒收录音；短 TTL 避免每帧查库
	bindOK        bool
	bindCheckedAt time.Time

	// calibBLEExpectOffline：enter_calib_ble ack 成功后置位，随后断开属主动离线（与意外掉线区分）
	calibBLEExpectOffline bool
}

// audioKindConnState 单通路（event/file）的段基准与补发缓存。
type audioKindConnState struct {
	baseTS     int64
	fragCache  map[int]fragCacheEntry
	lastCodec  string
	lastTotal  int
	lastPID    string
	lastBaseTS int64
	// lastSeq / sawSeq：同段内首传到达序（仅观测；跳号等补发窗口后再告警）
	lastSeq int
	sawSeq  bool
	// firstPassHole：首传出现过 seq 跳号（补发前属正常，不立刻记 gap_warn）
	firstPassHole bool

	// 段诊断（主要用于 kind=0 事件流排丢帧：上传丢 vs 网关滤）
	segRecvOK      int
	segDropCRC     int
	segDropDedup   int
	segDropAccept  int
	segDropOther   int
	segGapWarn     int
	segDeviceTotal int
	segSeqSeen     map[int]struct{}
	segStartedAt   time.Time
}

// audioPersistJob 读循环交给 persistPump 的已校验音频分片。
type audioPersistJob struct {
	meta   *model.DeviceUplink
	opus   []byte
	absTS  int64
	offset int64
	kind   int
}

const audioPersistQueueSize = 256

// fragCacheEntry 本连接近期音频分片元数据。
type fragCacheEntry struct {
	Codec string
	Total int
	TS    int64 // 绝对 Unix 微秒
	PID   string
}

// ConnManager 设备 SN 与连接映射管理器，协程安全。
type ConnManager struct {
	mu    sync.RWMutex
	conns map[string]*DeviceConn

	cfg      *config.Config
	redis    *redisx.Client
	kafka    kafkax.Publisher
	store    *store.Store
	pipeline *pipeline.Service
	log      *zap.Logger
	otaStop  chan struct{}

	// OTA 进度驱动重推（per-SN 内存态；落定后清除）
	otaMu       sync.Mutex
	otaSessions map[string]*otaSession

	// 每设备 TTS 串行队列（pause/resume/level 流控）
	ttsMu sync.Mutex
	tts   map[string]*ttsSender
}

// NewConnManager 创建连接管理器。store/pipeline 可为 nil。
func NewConnManager(cfg *config.Config, rdb *redisx.Client, kp kafkax.Publisher, st *store.Store, pipe *pipeline.Service, log *zap.Logger) *ConnManager {
	m := &ConnManager{
		conns:       make(map[string]*DeviceConn),
		cfg:         cfg,
		redis:       rdb,
		kafka:       kp,
		store:       st,
		pipeline:    pipe,
		log:         log,
		otaStop:     make(chan struct{}),
		otaSessions: make(map[string]*otaSession),
	}
	if st != nil {
		go m.watchOTATimeouts()
	}
	return m
}

// Get 按 SN 获取在线连接。
func (m *ConnManager) Get(sn string) (*DeviceConn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conns[sn]
	return c, ok
}

// Count 返回当前本地在线连接数。
func (m *ConnManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.conns)
}

// OnlineList 本进程 WebSocket 在线列表（实现 api.DeviceHub）。
func (m *ConnManager) OnlineList() []api.OnlineConn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]api.OnlineConn, 0, len(m.conns))
	for sn, dc := range m.conns {
		if dc == nil {
			continue
		}
		out = append(out, api.OnlineConn{
			SN:          sn,
			ConnID:      dc.ConnID,
			ConnectedAt: dc.ConnectedAt.UnixMilli(),
		})
	}
	return out
}

// Register 注册新连接；同 SN 已存在则返回旧连接供踢除。
func (m *ConnManager) Register(dc *DeviceConn) *DeviceConn {
	m.mu.Lock()
	defer m.mu.Unlock()

	var old *DeviceConn
	if prev, ok := m.conns[dc.SN]; ok && prev != nil && prev.ConnID != dc.ConnID {
		old = prev
		delete(m.conns, dc.SN)
	}
	m.conns[dc.SN] = dc
	return old
}

// Unregister 注销连接；仅 ConnID 匹配时删除，返回是否删除成功。
func (m *ConnManager) Unregister(sn, connID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conns[sn]; ok && c.ConnID == connID {
		delete(m.conns, sn)
		return true
	}
	return false
}

// CloseAll 主动关闭全部连接。
func (m *ConnManager) CloseAll() {
	if m.otaStop != nil {
		select {
		case <-m.otaStop:
		default:
			close(m.otaStop)
		}
	}
	m.mu.Lock()
	list := make([]*DeviceConn, 0, len(m.conns))
	for _, c := range m.conns {
		list = append(list, c)
	}
	m.conns = make(map[string]*DeviceConn)
	m.mu.Unlock()

	m.ttsMu.Lock()
	for sn, s := range m.tts {
		delete(m.tts, sn)
		if s != nil {
			s.close()
		}
	}
	m.ttsMu.Unlock()

	for _, c := range list {
		c.Close()
	}
}

// Close 安全关闭单连接。
func (dc *DeviceConn) Close() {
	dc.once.Do(func() {
		close(dc.CloseCh)
		_ = dc.Conn.Close()
	})
}

// TouchPong 更新最近一次心跳活动时间。
func (dc *DeviceConn) TouchPong() {
	dc.mu.Lock()
	dc.LastPong = time.Now()
	dc.mu.Unlock()
}

// MarkClientSeen 标记已收到设备侧数据（Ping/业务帧等）。
func (dc *DeviceConn) MarkClientSeen() {
	dc.mu.Lock()
	dc.clientSeen = true
	dc.LastPong = time.Now()
	dc.mu.Unlock()
}

// ClientSeen 是否已收到过设备侧数据。
func (dc *DeviceConn) ClientSeen() bool {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return dc.clientSeen
}

// GetLastPong 读取最近心跳活动时间。
func (dc *DeviceConn) GetLastPong() time.Time {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return dc.LastPong
}

// setPendingMeta 设置等待二进制的元数据。
func (dc *DeviceConn) setPendingMeta(meta *model.DeviceUplink) {
	dc.mu.Lock()
	dc.pendingMeta = meta
	dc.mu.Unlock()
}

// hasPendingMeta 是否正等待音频/补发 Binary（与前一条 Text 算一对限流）。
func (dc *DeviceConn) hasPendingMeta() bool {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return dc.pendingMeta != nil
}

// takePendingMeta 取出并清空等待中的元数据。
func (dc *DeviceConn) takePendingMeta() *model.DeviceUplink {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	meta := dc.pendingMeta
	dc.pendingMeta = nil
	return meta
}
