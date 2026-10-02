package page

import "go.uber.org/zap"

// NewManagerWithRefStart 让外部测试包(真 Chrome 集成测试)在断言快照原文时从固定的引用编号起点开始。
func NewManagerWithRefStart(cdp CDP, log *zap.Logger, refStart uint64) *Manager {
	return newManager(cdp, log, refStart)
}
