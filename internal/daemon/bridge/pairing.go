package bridge

import (
	"errors"
	"time"

	"github.com/scriptscat/sctl/internal/daemon/auth"
)

// pendingEnrollment 是一次进行中的扩展接入窗口(由 sctl connect 打开)。接入是唯一需要带外
// 配对码的环节:sctl 生成一次性码只在终端显示,用户输入扩展页面证明掌握,据此建立长期密钥 K。
type pendingEnrollment struct {
	code         string
	expiresAt    time.Time
	attemptsLeft int
}

// BeginEnrollment 打开一次接入窗口,返回展示形配对码(XXXX-XXXX)。供 sctl connect 使用;
// 码只在终端显示、绝不经 WS 下发给任何连接(docs/threat-model.md)。
func (s *Server) BeginEnrollment() (display string, err error) {
	canonical, display, err := auth.NewPairingCode()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.enrollment = &pendingEnrollment{
		code:         canonical,
		expiresAt:    time.Now().Add(s.enrollTTL),
		attemptsLeft: 3,
	}
	s.mu.Unlock()
	return display, nil
}

func (s *Server) activeEnrollmentCode() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enrollment == nil {
		return "", errors.New("no enrollment window in progress")
	}
	if time.Now().After(s.enrollment.expiresAt) {
		s.enrollment = nil
		return "", errors.New("enrollment window expired")
	}
	return s.enrollment.code, nil
}

// failEnrollmentAttempt 记一次接入失败,累计 3 次即作废接入窗口(docs/protocol.md §3.2)。
func (s *Server) failEnrollmentAttempt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enrollment == nil {
		return
	}
	s.enrollment.attemptsLeft--
	if s.enrollment.attemptsLeft <= 0 {
		s.enrollment = nil
	}
}

func (s *Server) clearEnrollment() {
	s.mu.Lock()
	s.enrollment = nil
	s.mu.Unlock()
}
