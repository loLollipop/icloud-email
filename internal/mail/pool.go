// IMAP 连接池: 按 Apple ID 复用长连接, 避免每次读信都 TLS+Login。
package mail

import (
	"fmt"
	"sync"
	"time"
)

// Pool 管理按账号复用的 IMAP 长连接。同一账号串行使用(go-imap 非并发安全)。
type Pool struct {
	mu    sync.Mutex
	items map[string]*pooledConn
	// idleClose 空闲超过该时间则下次使用前重建; 0 表示不主动关。
	idleClose time.Duration
	// afterLookup is a deterministic concurrency-test hook. Set it before any
	// concurrent use; production pools leave it nil.
	afterLookup func(key string)
}

type pooledConn struct {
	mu       sync.Mutex
	username string
	password string
	server   string
	port     int
	client   *Client
	lastUsed time.Time
}

// NewPool 创建连接池。
func NewPool() *Pool {
	return &Pool{
		items:     make(map[string]*pooledConn),
		idleClose: 10 * time.Minute,
	}
}

// Do 借出已连接的 Client 执行 fn; 用完不 Logout, 连接留在池中。
func (p *Pool) Do(appleID, appPassword string, fn func(*Client) error) error {
	return p.DoConfig("icloud:"+appleID, appleID, appPassword, IMAPServer, IMAPPort, fn)
}

// DoConfig reuses a connection under a stable account key. Any credential or
// server change closes the old connection before creating the replacement.
func (p *Pool) DoConfig(key, username, password, server string, port int, fn func(*Client) error) error {
	if key == "" || username == "" || password == "" || server == "" || port < 1 || port > 65535 {
		return fmt.Errorf("IMAP 凭据为空")
	}
	pc := p.lockOrCreate(key, username, password, server, port)
	defer pc.mu.Unlock()

	if err := pc.ensure(p.idleClose); err != nil {
		return err
	}
	err := fn(pc.client)
	pc.lastUsed = time.Now()
	if err != nil && isLikelyConnErr(err) {
		// 连接坏了, 丢掉, 下次重建
		pc.client.forceClose()
		pc.client = nil
	}
	return err
}

// Drop closes and removes one stable account key from the pool.
func (p *Pool) Drop(key string) {
	p.mu.Lock()
	pc, ok := p.items[key]
	if !ok {
		p.mu.Unlock()
		return
	}
	delete(p.items, key)
	p.mu.Unlock()

	pc.mu.Lock()
	if pc.client != nil {
		pc.client.forceClose()
		pc.client = nil
	}
	pc.mu.Unlock()
}

// Close 关闭池内全部连接。
func (p *Pool) Close() {
	p.mu.Lock()
	items := p.items
	p.items = make(map[string]*pooledConn)
	p.mu.Unlock()

	for _, pc := range items {
		pc.mu.Lock()
		if pc.client != nil {
			pc.client.forceClose()
			pc.client = nil
		}
		pc.mu.Unlock()
	}
}

// lockOrCreate returns the configured slot with pc.mu held. The pool mutex is
// never held while waiting for a busy account slot, so one slow account cannot
// block lookups for other accounts. After taking pc.mu, map membership is
// rechecked because Drop or Close may have detached that slot in the meantime.
func (p *Pool) lockOrCreate(key, username, password, server string, port int) *pooledConn {
	for {
		p.mu.Lock()
		pc, ok := p.items[key]
		if !ok {
			pc = &pooledConn{username: username, password: password, server: server, port: port}
			p.items[key] = pc
		}
		p.mu.Unlock()

		if p.afterLookup != nil {
			p.afterLookup(key)
		}
		pc.mu.Lock()

		p.mu.Lock()
		current := p.items[key]
		p.mu.Unlock()
		if current != pc {
			pc.mu.Unlock()
			continue
		}

		if pc.username != username || pc.password != password || pc.server != server || pc.port != port {
			if pc.client != nil {
				pc.client.forceClose()
				pc.client = nil
			}
			pc.username = username
			pc.password = password
			pc.server = server
			pc.port = port
		}
		return pc
	}
}

func (pc *pooledConn) ensure(idleClose time.Duration) error {
	if pc.client != nil {
		// 空闲太久主动重建, 避免服务端静默断连
		if idleClose > 0 && !pc.lastUsed.IsZero() && time.Since(pc.lastUsed) > idleClose {
			pc.client.forceClose()
			pc.client = nil
		}
	}
	if pc.client != nil {
		if err := pc.client.Ping(); err == nil {
			return nil
		}
		pc.client.forceClose()
		pc.client = nil
	}
	c := NewClientWithServer(pc.username, pc.password, pc.server, pc.port)
	if err := c.Connect(); err != nil {
		return err
	}
	pc.client = c
	pc.lastUsed = time.Now()
	return nil
}

func isLikelyConnErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// 常见断连/IO 错误关键字
	for _, k := range []string{
		"connection reset", "connection closed", "broken pipe", "EOF", "i/o timeout",
		"use of closed", "not connected", "connection refused",
		"IMAP 连接", "wsarecv", "wsasend",
	} {
		if containsFold(s, k) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		len(sub) == 0 ||
		indexFold(s, sub) >= 0)
}

func indexFold(s, sub string) int {
	// 小写 ASCII 子串查找, 够用
	sl := toLowerASCII(s)
	subl := toLowerASCII(sub)
	for i := 0; i+len(subl) <= len(sl); i++ {
		if sl[i:i+len(subl)] == subl {
			return i
		}
	}
	return -1
}

func toLowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
