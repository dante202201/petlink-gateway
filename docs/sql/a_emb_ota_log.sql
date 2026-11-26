-- 设备 OTA 升级判定日志（MySQL）
-- 若网关已开 database.enabled，启动时也会 CREATE IF NOT EXISTS；也可手工执行本脚本。

CREATE TABLE IF NOT EXISTS a_emb_ota_log (
  id BIGINT NOT NULL AUTO_INCREMENT,
  sn VARCHAR(64) NOT NULL COMMENT '设备序列号',
  from_fw VARCHAR(64) NOT NULL DEFAULT '' COMMENT '下发前设备当前版本',
  target_fw VARCHAR(64) NOT NULL DEFAULT '' COMMENT '目标版本',
  url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '固件 URL',
  force_flag TINYINT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL DEFAULT 'pushed' COMMENT 'pushed/confirming/success/fail',
  fail_reason VARCHAR(64) NOT NULL DEFAULT '',
  disconnected TINYINT NOT NULL DEFAULT 0 COMMENT 'ack 后是否断过线',
  reconnected TINYINT NOT NULL DEFAULT 0 COMMENT '是否已重连',
  result_fw VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'boot ping 上报的 fw',
  pushed_at BIGINT NOT NULL DEFAULT 0 COMMENT 'Unix 毫秒',
  ack_at BIGINT NOT NULL DEFAULT 0,
  disconnect_at BIGINT NOT NULL DEFAULT 0,
  reconnect_at BIGINT NOT NULL DEFAULT 0,
  boot_at BIGINT NOT NULL DEFAULT 0,
  result_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (id),
  KEY idx_ota_log_sn (sn, id),
  KEY idx_ota_log_status (status, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='设备 OTA 升级判定日志';
