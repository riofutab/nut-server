package master

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
	"nut-server/internal/security"
)

const testEnrollmentToken = "enroll-secret"

type wireSlave struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

// serveConns runs handleConn for every connection to a loopback listener and
// returns its address; cleanup closes the listener and waits for handlers.
func serveConns(t *testing.T, server *Server) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.trackConn(conn)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer server.untrackConn(conn)
				server.handleConn(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		server.closeActiveConns()
		wg.Wait()
	})
	return listener.Addr().String()
}

func dialWireSlave(t *testing.T, addr string) *wireSlave {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &wireSlave{conn: conn, reader: bufio.NewReader(conn), writer: bufio.NewWriter(conn)}
}

func (f *wireSlave) send(t *testing.T, messageType string, payload interface{}) {
	t.Helper()
	if err := protocol.WriteEnvelope(f.writer, messageType, payload); err != nil {
		t.Fatalf("send %s: %v", messageType, err)
	}
}

func (f *wireSlave) read() (protocol.Envelope, error) {
	_ = f.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	return protocol.ReadEnvelope(f.reader)
}

func (f *wireSlave) register(t *testing.T, nodeID, token string) protocol.RegisterAckMessage {
	t.Helper()
	f.send(t, protocol.TypeRegister, protocol.RegisterMessage{NodeID: nodeID, Hostname: nodeID + ".lan", Token: token})
	env, err := f.read()
	if err != nil {
		t.Fatalf("read register ack: %v", err)
	}
	if env.Type != protocol.TypeRegisterAck {
		t.Fatalf("got %s want register_ack", env.Type)
	}
	var ack protocol.RegisterAckMessage
	decodePayloadForTest(t, env.Data, &ack)
	return ack
}

func (f *wireSlave) pingAndWaitPong(t *testing.T) {
	t.Helper()
	f.send(t, protocol.TypePing, protocol.PingMessage{SentAt: time.Now().UTC()})
	env, err := f.read()
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if env.Type != protocol.TypePong {
		t.Fatalf("got %s want pong", env.Type)
	}
}

func registerOnce(t *testing.T, addr, nodeID, token string) protocol.RegisterAckMessage {
	t.Helper()
	ack := dialWireSlave(t, addr).register(t, nodeID, token)
	return ack
}

func newEnrollmentServer(t *testing.T, enrollment string) *Server {
	t.Helper()
	return NewServer(config.MasterConfig{
		AdminToken:       "admin",
		EnrollmentTokens: []string{"other-enroll-token", testEnrollmentToken},
		Enrollment:       enrollment,
		StateFile:        filepath.Join(t.TempDir(), "master-state.json"),
	})
}

// enrollNode runs a full enrollment (register, then the confirming envelope)
// and returns the issued token plus the still-open connection.
func enrollNode(t *testing.T, addr, nodeID string) (string, *wireSlave) {
	t.Helper()
	slave := dialWireSlave(t, addr)
	ack := slave.register(t, nodeID, testEnrollmentToken)
	if !ack.Accepted || ack.IssuedToken == "" {
		t.Fatalf("expected accepted enrollment with issued token, got %+v", ack)
	}
	slave.pingAndWaitPong(t)
	return ack.IssuedToken, slave
}

func nodeEnrolled(server *Server, nodeID string) bool {
	for _, node := range server.Status().Nodes {
		if node.NodeID == nodeID {
			return node.Enrolled
		}
	}
	return false
}

func waitForCondition(t *testing.T, label string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

func TestEnrollmentIssuesTokenThatBecomesNodeTokenOnFirstEnvelope(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	addr := serveConns(t, server)
	slave := dialWireSlave(t, addr)

	ack := slave.register(t, "node-1", testEnrollmentToken)

	if !ack.Accepted {
		t.Fatalf("expected enrollment accepted, got %+v", ack)
	}
	if len(ack.IssuedToken) != 64 {
		t.Fatalf("issued token length=%d want 64 hex chars", len(ack.IssuedToken))
	}
	if nodeEnrolled(server, "node-1") {
		t.Fatal("node must not be enrolled before the slave confirms the issued token")
	}

	slave.pingAndWaitPong(t)

	if !nodeEnrolled(server, "node-1") {
		t.Fatal("node should be enrolled after the first envelope following the ack")
	}
	content, err := os.ReadFile(server.cfg.StateFile)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if strings.Contains(string(content), ack.IssuedToken) {
		t.Fatal("state file must not contain the raw issued token")
	}
	if !strings.Contains(string(content), security.HashToken(ack.IssuedToken)) {
		t.Fatal("state file should contain the issued token hash")
	}
}

func TestEnrolledNodeTokenSurvivesMasterRestart(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	issued, _ := enrollNode(t, serveConns(t, server), "node-1")

	restarted := NewServer(server.cfg)
	addr := serveConns(t, restarted)

	if ack := registerOnce(t, addr, "node-1", issued); !ack.Accepted || ack.IssuedToken != "" {
		t.Fatalf("issued token should authenticate after restart without a new token, got %+v", ack)
	}
	if ack := registerOnce(t, addr, "node-1", testEnrollmentToken); ack.Accepted {
		t.Fatal("enrollment token must stay rejected for the node after restart")
	}
}

func TestEnrollmentDiscardsPendingTokenWhenConnectionDropsBeforeConfirm(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	addr := serveConns(t, server)
	slave := dialWireSlave(t, addr)
	first := slave.register(t, "node-1", testEnrollmentToken)

	_ = slave.conn.Close()
	waitForCondition(t, "connection released", func() bool {
		_, ok := server.registry.Get("node-1")
		return !ok
	})

	if nodeEnrolled(server, "node-1") {
		t.Fatal("pending token must be discarded when the connection drops")
	}
	if ack := registerOnce(t, addr, "node-1", first.IssuedToken); ack.Accepted {
		t.Fatal("an unconfirmed issued token must not authenticate")
	}
	second := registerOnce(t, addr, "node-1", testEnrollmentToken)
	if !second.Accepted || second.IssuedToken == "" || second.IssuedToken == first.IssuedToken {
		t.Fatalf("node should be able to enroll again with a fresh token, got %+v", second)
	}
}

func TestEnrollmentTokenCannotRegisterAsEnrolledNode(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	addr := serveConns(t, server)
	issued, owner := enrollNode(t, addr, "node-1")

	ack := registerOnce(t, addr, "node-1", testEnrollmentToken)

	if ack.Accepted || ack.Message != "invalid token" {
		t.Fatalf("enrollment token must be rejected for an enrolled node, got %+v", ack)
	}
	owner.pingAndWaitPong(t)
	if ack := registerOnce(t, addr, "node-1", issued); !ack.Accepted || ack.IssuedToken != "" {
		t.Fatalf("issued token should register without issuing another, got %+v", ack)
	}
}

func TestEnrollmentRejectsUnknownToken(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	addr := serveConns(t, server)

	ack := registerOnce(t, addr, "node-1", "not-an-enrollment-token")

	if ack.Accepted || ack.Message != "invalid token" {
		t.Fatalf("got %+v want invalid token", ack)
	}
}

func TestClosedEnrollmentRejectsNewNodesButAcceptsEnrolledOnes(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentClosed)
	issued := server.issueNodeToken("node-1")
	addr := serveConns(t, server)

	newNode := registerOnce(t, addr, "node-2", testEnrollmentToken)
	enrolled := registerOnce(t, addr, "node-1", issued)

	if newNode.Accepted || newNode.Message != "enrollment closed" {
		t.Fatalf("got %+v want enrollment closed", newNode)
	}
	if !enrolled.Accepted {
		t.Fatalf("enrolled node should register while enrollment is closed, got %+v", enrolled)
	}
}

func revokeRequest(server *Server, nodeID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/nodes/"+nodeID+"/token", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	server.adminMux().ServeHTTP(rec, req)
	return rec
}

func TestRevokeTokenDisconnectsNodeAndAllowsReEnrollment(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	addr := serveConns(t, server)
	issued, owner := enrollNode(t, addr, "node-1")

	rec := revokeRequest(server, "node-1")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := owner.read(); err == nil {
		t.Fatal("revoking the token should close the node's connection")
	}
	if nodeEnrolled(server, "node-1") {
		t.Fatal("node should not be enrolled after revoke")
	}
	if ack := registerOnce(t, addr, "node-1", issued); ack.Accepted || ack.Message != "invalid token" {
		t.Fatalf("revoked token must be rejected with invalid token, got %+v", ack)
	}
	reissued, _ := enrollNode(t, addr, "node-1")
	if reissued == issued {
		t.Fatal("re-enrollment must issue a new token")
	}
}

func TestRevokeTokenReturnsNotFoundWithoutToken(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)

	rec := revokeRequest(server, "node-1")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
}

func TestRevokeTokenRequiresAdminToken(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	req := httptest.NewRequest(http.MethodDelete, "/nodes/node-1/token", nil)
	rec := httptest.NewRecorder()

	server.adminMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestUnsetExpectedRouteStillWorksNextToTokenRoute(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	server.directory.SetExpected("token", "", nil)
	req := httptest.NewRequest(http.MethodDelete, "/nodes/expect/token", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()

	server.adminMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if meta, _ := server.directory.Get("token"); meta.Expected {
		t.Fatal("expected flag should be cleared")
	}
}

func TestDeleteNodeDropsItsIssuedToken(t *testing.T) {
	server := newEnrollmentServer(t, config.EnrollmentOpen)
	server.directory.SetExpected("printer", "", nil)
	server.issueNodeToken("printer")
	req := httptest.NewRequest(http.MethodDelete, "/nodes/printer", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()

	server.adminMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	if server.isEnrolled("printer") {
		t.Fatal("deleting a node must drop its issued token")
	}
}
