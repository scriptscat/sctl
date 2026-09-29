//go:build !unix

package pagetest

import "os/exec"

// newLifeline 在非 Unix 系统上什么都不做:Windows 的 exec.Cmd 不支持 ExtraFiles,Chrome 的调试管道接不上,
// 只能靠 Cleanup 与 go test 期限前的看门狗收拾 Chrome。
func newLifeline(*exec.Cmd) (started, release func(), err error) {
	return func() {}, func() {}, nil
}
