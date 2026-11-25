package rocketmq

import (
	"os"

	mq5 "github.com/apache/rocketmq-clients/golang/v5"
)

func ConfigureProxyLogger() error {
	// SDK 的日志开关通过进程环境传入；应用启动时固定输出到标准输出。
	if err := os.Setenv(mq5.ENABLE_CONSOLE_APPENDER, "true"); err != nil {
		return err
	}
	if err := os.Setenv(mq5.CLIENT_LOG_LEVEL, "warn"); err != nil {
		return err
	}
	mq5.ResetLogger()
	return nil
}
