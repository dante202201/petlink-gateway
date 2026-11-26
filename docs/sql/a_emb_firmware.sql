-- 固件 OTA 包历史表（MySQL）
-- 若网关已开 database.enabled，启动时也会 CREATE IF NOT EXISTS；也可手工执行本脚本。

CREATE TABLE IF NOT EXISTS a_emb_firmware (
  id INT NOT NULL AUTO_INCREMENT,
  version VARCHAR(64) NOT NULL DEFAULT '' COMMENT '固件版本号',
  url VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'OSS/CDN 直链',
  size BIGINT NOT NULL DEFAULT 0 COMMENT '字节数',
  md5 VARCHAR(32) NOT NULL DEFAULT '' COMMENT '整包 MD5',
  force_flag TINYINT NOT NULL DEFAULT 0 COMMENT '1=强制升级',
  changelog VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '更新说明',
  filename VARCHAR(255) NOT NULL DEFAULT '' COMMENT '原始文件名',
  is_current TINYINT NOT NULL DEFAULT 0 COMMENT '1=当前对设备生效',
  created_at INT NOT NULL DEFAULT 0 COMMENT 'Unix 秒',
  PRIMARY KEY (id),
  KEY idx_firmware_current (is_current, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='固件 OTA 包历史';
