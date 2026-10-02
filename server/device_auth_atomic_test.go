package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDeviceAuthAtomicApproval(t *testing.T) {
	s := newDeviceAuthStore()
	p := s.Create(deviceAuthMetadata{AgentID: "machine"})
	p.TenantID = "tampered"
	if snap, _ := s.snapshot(p.Code); snap.TenantID != "" { t.Fatal("Create exposed mutable state") }
	issue := func() (string, error) { return "", errors.New("DB failed") }
	if _, err := s.approve(p.Code, "", "tenant", "Tenant", "", "operator", issue); err == nil { t.Fatal("issuance failure ignored") }
	if snap, _ := s.snapshot(p.Code); snap.Status != deviceAuthStatusPending || snap.JoinToken != "" { t.Fatal("failed issuance stranded approval") }
	var issued atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.approve(p.Code, "", "tenant", "Tenant", "", "operator", func() (string, error) { issued.Add(1); return "token", nil })
		}()
	}
	close(start)
	wg.Wait()
	if issued.Load() != 1 { t.Fatalf("issued %d tokens", issued.Load()) }
	poll, _ := s.getByPoll(p.PollToken)
	poll.JoinToken = "tampered"
	if snap, _ := s.snapshot(p.Code); snap.JoinToken != "token" { t.Fatal("poll exposed mutable state") }
	if _, err := s.reject(p.Code, "reject", "loser", "tenant"); err == nil { t.Fatal("rejection overwrote approval") }
}

func TestDeviceAuthApprovalRejectRace(t *testing.T) {
	for i := 0; i < 25; i++ {
		s := newDeviceAuthStore()
		p := s.Create(deviceAuthMetadata{})
		var issued atomic.Int32
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); s.approve(p.Code, "", "tenant", "Tenant", "", "operator", func() (string, error) { issued.Add(1); return "token", nil }) }()
		go func() { defer wg.Done(); s.reject(p.Code, "reject", "operator", "") }()
		wg.Wait()
		snap, _ := s.snapshot(p.Code)
		if snap.Status == deviceAuthStatusRejected && issued.Load() != 0 { t.Fatal("rejected request leaked join token") }
	}
}