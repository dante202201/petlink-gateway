// 极简本地测试客户端：按固件协议模拟握手、定位、双帧音频、补发。
//
// 用法：
//
//	go run ./cmd/demo_client -addr "wss://127.0.0.1:8443/ws?sn=TEST_SN_001&token=test-token-001"
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/websocket"

	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/util"
)

func main() {
	addr := flag.String("addr", "", "完整 WSS 地址（含 query）；为空则用 host+sn+token 拼接")
	host := flag.String("host", "wss://127.0.0.1:8443/ws", "网关路径（不含 query）")
	sn := flag.String("sn", "TEST_SN_001", "设备 SN")
	token := flag.String("token", "test-token-001", "设备 Token")
	insecure := flag.Bool("insecure", true, "跳过证书校验（本地自签）")
	flag.Parse()

	wsURL := *addr
	if wsURL == "" {
		wsURL = fmt.Sprintf("%s?sn=%s&token=%s", *host, *sn, *token)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: *insecure, //nolint:gosec
		},
	}

	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			log.Fatalf("连接失败: %v, HTTP=%d", err, resp.StatusCode)
		}
		log.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()
	log.Printf("已连接 %s", wsURL)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				log.Printf("读协程退出: %v", err)
				return
			}
			switch mt {
			case websocket.TextMessage:
				log.Printf("收到下行指令: %s", string(data))
			case websocket.BinaryMessage:
				log.Printf("收到 TTS Binary: %d bytes", len(data))
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, []byte("hw"), time.Now().Add(5*time.Second)); err != nil {
					log.Printf("Ping 失败: %v", err)
					return
				}
				log.Printf("已发送硬件 Ping")
			}
		}
	}()

	// 1) 独立定位 type=locate
	loc := model.DeviceUplink{
		Type:    "locate",
		SN:      *sn,
		Token:   *token,
		Bat:     86,
		RSSI:    -55,
		TS:      util.NowUTCUnixMicro(),
		LocType: model.LocTypeWiFi,
		LocMacs: model.LocMacList{
			{MAC: "AA:BB:CC:DD:EE:01", RSSI: -40, SSID: "Home"},
			{MAC: "AA:BB:CC:DD:EE:02", RSSI: -55},
		},
		LocTS: util.NowUTCUnixMicro(),
	}
	locBytes, _ := json.Marshal(loc)
	if err := conn.WriteMessage(websocket.TextMessage, locBytes); err != nil {
		log.Fatalf("发送定位失败: %v", err)
	}
	log.Printf("已发送 type=locate 定位包")

	// 2) 双帧音频：JSON 文本 + OPUS 二进制（段首绝对 Unix µs，后续相对偏移 µs）
	total := 3
	baseTS := util.NowUTCUnixMicro()
	for seq := 0; seq < total; seq++ {
		opus := []byte(fmt.Sprintf("FAKE_OPUS_CHUNK_%d", seq))
		ts := baseTS
		if seq > 0 {
			ts = int64(seq) * 20_000 // 20ms → 微秒
		}
		end := util.Bool01(false)
		if seq == total-1 {
			end = true
		}
		meta := model.DeviceUplink{
			Type:     "audio",
			SN:       *sn,
			Token:    *token,
			ProtoVer: 1,
			Codec:    "opus",
			Seq:      seq,
			Total:    total,
			End:      end,
			PID:      "pet_10086",
			DB:       -45,
			Bat:      86,
			Charge:   false,
			RSSI:     -55,
			TS:       ts,
			CRC:      util.CRC32IEEE(opus),
		}
		metaBytes, _ := json.Marshal(meta)
		if err := conn.WriteMessage(websocket.TextMessage, metaBytes); err != nil {
			log.Fatalf("发送音频 JSON 失败: %v", err)
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, opus); err != nil {
			log.Fatalf("发送音频 Binary 失败: %v", err)
		}
		log.Printf("已发送双帧音频 seq=%d/%d", seq, total-1)
		time.Sleep(400 * time.Millisecond)
	}

	// 3) 补发重复分片（应去重）
	opus0 := []byte("FAKE_OPUS_CHUNK_0")
	retrans := model.DeviceUplink{
		Type:  "retransmit",
		SN:    *sn,
		Token: *token,
		Codec: "opus",
		Seq:   0,
		Total: total,
		PID:   "pet_10086",
		DB:    -45,
		Bat:   86,
		RSSI:  -55,
		TS:    baseTS,
		CRC:   util.CRC32IEEE(opus0),
	}
	rb, _ := json.Marshal(retrans)
	_ = conn.WriteMessage(websocket.TextMessage, rb)
	_ = conn.WriteMessage(websocket.BinaryMessage, opus0)
	log.Printf("已发送补发重复分片（预期被去重）")

	log.Printf("演示完成，保持连接；Ctrl+C 退出")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	select {
	case <-sig:
	case <-done:
	}
}
