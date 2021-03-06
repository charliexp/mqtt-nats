package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/tada/mqtt-nats/test/packet"

	"github.com/nats-io/nats.go"
	"github.com/tada/mqtt-nats/logger"
	"github.com/tada/mqtt-nats/mqtt"
	"github.com/tada/mqtt-nats/mqtt/pkg"
	"github.com/tada/mqtt-nats/test/mock"
	"github.com/tada/mqtt-nats/test/utils"
)

type mockServer struct {
	sm
	pkg.IDManager
	nc        *nats.Conn
	ncError   error
	willError error
	t         *testing.T
}

func (m *mockServer) UnmarshalFromJSON(js *json.Decoder, firstToken json.Token) {
	m.t.Helper()
	m.t.Fatal("implement me")
}

func (m *mockServer) MarshalToJSON(io.Writer) {
	m.t.Helper()
	m.t.Fatal("implement me")
}

func (m *mockServer) SessionManager() SessionManager {
	return m
}

func (m *mockServer) ManageClient(c Client) {
}

func (m *mockServer) NatsConn(creds *pkg.Credentials) (*nats.Conn, error) {
	return m.nc, m.ncError
}

func (m *mockServer) HandleRetain(pp *pkg.Publish) *pkg.Publish {
	return pp
}

func (m *mockServer) PublishMatching(sp *pkg.Subscribe, c Client) {
}

func (m *mockServer) PublishWill(will *pkg.Will, creds *pkg.Credentials) error {
	return m.willError
}

func newMockServer(t *testing.T) *mockServer {
	return &mockServer{sm: sm{m: make(map[string]Session, 3)}, IDManager: pkg.NewIDManager(), t: t}
}

func writePacket(t *testing.T, p pkg.Packet, w io.Writer) {
	t.Helper()
	mw := mqtt.NewWriter()
	p.Write(mw)
	_, err := w.Write(mw.Bytes())
	utils.CheckNotError(err, t)
}

var silent = logger.New(logger.Silent, nil, nil)

// Test_client_String tests that the client String method produces sane output
// in all states of the client (infant, connected, disconnected)
func Test_client_String(t *testing.T) {
	conn := mock.NewConnection()
	cl := NewClient(newMockServer(t), silent, conn)
	done := make(chan bool, 1)
	go func() {
		cl.Serve()
		done <- true
	}()

	utils.CheckEqual("Client (not yet connected)", cl.(fmt.Stringer).String(), t)

	rConn := conn.Remote()
	writePacket(t, pkg.NewConnect("client-id", false, 1, nil, nil), rConn)
	bs := make([]byte, 2)
	_, err := rConn.Read(bs)
	utils.CheckNotError(err, t)
	utils.CheckEqual("Client client-id", cl.(fmt.Stringer).String(), t)
	writePacket(t, pkg.DisconnectSingleton, rConn)
	<-done
	utils.CheckEqual("Client client-id (disconnected)", cl.(fmt.Stringer).String(), t)
}

// Test_client_natsConnError tests that the server responds with a ConnAck containing
// an pkg.RtServerUnavailable when the client was unable to establish a NATS connection.
func Test_client_natsConnError(t *testing.T) {
	conn := mock.NewConnection()
	rConn := conn.Remote()
	ms := newMockServer(t)
	ms.ncError = errors.New("unauthorized")
	cl := NewClient(ms, silent, conn)
	go cl.Serve()

	writePacket(t, pkg.NewConnect("client-id", false, 1, nil, nil), rConn)
	ca, ok := packet.Parse(t, rConn).(*pkg.ConnAck)
	utils.CheckTrue(ok, t)
	utils.CheckEqual(pkg.RtServerUnavailable, ca.ReturnCode(), t)
}

// Test_client_natsConnError tests that the server responds with a ConnAck containing
// an pkg.RtServerUnavailable when the client was unable to establish a NATS connection.
func Test_client_natsSubscribeError(t *testing.T) {
	mt := &collectLogsT{}
	conn := mock.NewConnection()
	ms := newMockServer(t)
	ms.ncError = nil
	ms.nc = &nats.Conn{}
	cl := NewClient(ms, utils.NewLogger(logger.Error, mt), conn)
	go cl.Serve()

	rConn := conn.Remote()
	writePacket(t, pkg.NewConnect("client-id", true, 1, nil, nil), rConn)
	ca, ok := packet.Parse(t, rConn).(*pkg.ConnAck)
	utils.CheckTrue(ok, t)
	utils.CheckEqual(pkg.RtAccepted, ca.ReturnCode(), t)

	// Newline is unacceptable in a subject
	writePacket(t, pkg.NewSubscribe(1, pkg.Topic{Name: "top\nic"}), rConn)
	sa, ok := packet.Parse(t, rConn).(*pkg.SubAck)
	utils.CheckTrue(ok, t)

	// Topic return code should be 0x80 to indicate failure
	utils.CheckEqual(pkg.NewSubAck(1, 0x80), sa, t)

	// At least one error should be logged (additional caused by forced disconnect)
	utils.CheckTrue(len(mt.logEntries) > 0, t)
	el := mt.logEntries[0]
	utils.CheckEqual(5, len(el), t)
	utils.CheckEqual(el[0], "ERROR", t)
	utils.CheckTrue(cl == el[1], t)
	utils.CheckEqual("NATS subscribe", el[2], t)
	utils.CheckEqual("top\nic", el[3], t)
}

type collectLogsT struct {
	logEntries [][]interface{}
}

func (m *collectLogsT) Log(args ...interface{}) {
	m.logEntries = append(m.logEntries, args)
}

func (m *collectLogsT) Helper() {
}

// Test_client_publishWillError tests that errors during an attempt to publish the will
// provided in the CONNECT package are logged at level logger.Error
func Test_client_publishWillError(t *testing.T) {
	mt := &collectLogsT{}
	conn := mock.NewConnection()
	ms := newMockServer(t)
	ms.willError = errors.New("unauthorized")
	cl := NewClient(ms, utils.NewLogger(logger.Error, mt), conn)

	done := make(chan bool, 1)
	go func() {
		cl.Serve()
		done <- true
	}()

	rConn := conn.Remote()
	writePacket(t, pkg.NewConnect("client-id", false, 1, &pkg.Will{
		Topic:   "some/will",
		Message: []byte("will message")}, nil), rConn)

	ca, ok := packet.Parse(t, rConn).(*pkg.ConnAck)
	utils.CheckEqual(pkg.RtAccepted, ca.ReturnCode(), t)
	utils.CheckTrue(ok, t)
	_ = conn.Close()
	<-done

	// At least one error should be logged (additional caused by forced disconnect)
	utils.CheckTrue(len(mt.logEntries) > 0, t)
	el := mt.logEntries[0]
	utils.CheckEqual(len(el), 3, t)
	utils.CheckEqual(el[0], "ERROR", t)
	utils.CheckTrue(cl == el[1], t)
	err, ok := el[2].(error)
	utils.CheckTrue(ok, t)
	utils.CheckEqual("unauthorized", err.Error(), t)
}

// Test_client_debugLog checks that the client performs debug logging
func Test_client_debugLog(t *testing.T) {
	mt := &collectLogsT{}
	conn := mock.NewConnection()
	cl := NewClient(newMockServer(t), utils.NewLogger(logger.Debug, mt), conn)

	done := make(chan bool, 1)
	go func() {
		cl.Serve()
		done <- true
	}()

	rConn := conn.Remote()
	writePacket(t, pkg.NewConnect("client-id", false, 1, nil, nil), rConn)
	ca, ok := packet.Parse(t, rConn).(*pkg.ConnAck)
	utils.CheckTrue(ok, t)
	utils.CheckEqual(pkg.RtAccepted, ca.ReturnCode(), t)
	writePacket(t, pkg.PubRec(1), rConn)
	writePacket(t, pkg.PubRel(2), rConn)
	writePacket(t, pkg.PubComp(3), rConn)
	writePacket(t, pkg.DisconnectSingleton, rConn)
	<-done

	// check that all received packages were logged
	cnt := 0
	for _, le := range mt.logEntries {
		if len(le) == 4 && le[0] == "DEBUG" && le[2] == "received" {
			switch le[3].(type) {
			case *pkg.Connect, pkg.PubRec, pkg.PubRel, pkg.PubComp:
				cnt++
			}
		}
	}
	utils.CheckEqual(4, cnt, t)
}

type writeFailure struct {
	*mock.Connection
	succeed uint
	tick    chan bool
}

func (c *writeFailure) Write(bs []byte) (int, error) {
	if c.succeed == 0 {
		return 0, errors.New("write failed")
	}
	i, err := c.Connection.Write(bs)
	c.succeed--
	c.tick <- true
	return i, err
}

// Test_write_failure_when_connected checks that the client propagates write error
func Test_write_failure_when_connected(t *testing.T) {
	conn := &writeFailure{Connection: mock.NewConnection()}
	mt := &collectLogsT{}
	cl := NewClient(newMockServer(t), utils.NewLogger(logger.Error, mt), conn)

	done := make(chan bool, 1)
	go func() {
		cl.Serve()
		done <- true
	}()

	writePacket(t, pkg.NewConnect("client-id", false, 1, nil, nil), conn.Remote())
	// Should fail with forced disconnect
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected forced disconnect did not occur")
	}

	// At least one error should be logged (additional caused by forced disconnect)
	utils.CheckTrue(len(mt.logEntries) > 0, t)
	el := mt.logEntries[0]
	utils.CheckEqual(len(el), 3, t)
	utils.CheckEqual(el[0], "ERROR", t)
	utils.CheckTrue(cl == el[1], t)
	err, ok := el[2].(error)
	utils.CheckTrue(ok, t)
	utils.CheckEqual("write failed", err.Error(), t)
}

// Test_write_failure_during_drain checks that the client logs error that occurs during writeLoop drain
func Test_write_failure_during_drain(t *testing.T) {
	conn := &writeFailure{Connection: mock.NewConnection(), succeed: 1, tick: make(chan bool, 1)}
	mt := &collectLogsT{}
	cl := NewClient(newMockServer(t), utils.NewLogger(logger.Error, mt), conn)

	done := make(chan bool, 1)
	go func() {
		cl.Serve()
		done <- true
	}()

	rConn := conn.Remote()
	writePacket(t, pkg.NewConnect("client-id", false, 1, nil, nil), rConn)
	<-conn.tick
	cl.(*client).queueForWrite(pkg.PingResponseSingleton)
	cl.(*client).queueForWrite(pkg.DisconnectSingleton)
	cl.SetDisconnected(nil)

	// Should fail with forced disconnect
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected forced disconnect did not occur")
	}

	// At least one error should be logged (additional caused by forced disconnect)
	utils.CheckTrue(len(mt.logEntries) > 0, t)
	el := mt.logEntries[0]
	utils.CheckEqual(len(el), 3, t)
	utils.CheckEqual(el[0], "ERROR", t)
	utils.CheckTrue(cl == el[1], t)
	err, ok := el[2].(error)
	utils.CheckTrue(ok, t)
	utils.CheckEqual("write failed", err.Error(), t)
}
