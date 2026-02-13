package gost

import (
	"net"
	"syscall"
	"strings"
	"time"
	"errors"
	"github.com/hashicorp/yamux"
	"golang.org/x/sys/windows"
    "crypto/rand"
    "encoding/hex"
)

type smbnpTransporter struct{
	pipeName string
	mux *yamux.Session
	path string
}

func SMBNPTransporter(url string) Transporter {
	parts := strings.SplitN(url, "/", 4)
	pipeName := parts[len(parts)-1]
	host := strings.Split(parts[2], ":")[0]
	path := `\\` + host + `\pipe\`

	return &smbnpTransporter{pipeName, nil, path}
}

func (tr *smbnpTransporter) Dial(addr string, options ...DialOption) (net.Conn, error) {

	if tr.mux == nil {
		conn, err := pipeDial(tr.path + tr.pipeName)
		if err != nil {
			return nil, err
		}

		conn.Write([]byte("0"))

		newNameB := make([]byte, 100)
		length, err := conn.Read(newNameB)
		if err != nil {
			return nil, err
		}
		new_name := string(newNameB[:length])

		conn2, err := pipeDial(tr.path + tr.pipeName)
		if err != nil {
			return nil, err
		}

		conn2.Write([]byte(new_name))
		conn2.Read(newNameB)

		conn_duplex := &PipeConnDuplex{tr.path + tr.pipeName, conn2, conn}

		tr.mux, err = yamux.Client(conn_duplex, nil)
		if err != nil {
			return nil, err
		}
	}

	conn, err := tr.mux.Open()
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func (tr *smbnpTransporter) Handshake(conn net.Conn, options ...HandshakeOption) (net.Conn, error) {
	return conn, nil
}

func (tr *smbnpTransporter) Multiplex() bool {
	return false
}

type smbnpListener struct {
	listener *ConnDispatcher
	pipeName string
}

func SMBNPListener(pipeName string) (Listener, error) {

	disp := createPipeDispatcher(pipeName)
	listener := &ConnDispatcher{disp, make(chan *yamux.Stream, 128)}
	go listener.listenLoop()

	return &smbnpListener{listener, pipeName}, nil
}

func (ln *smbnpListener) Accept() (c net.Conn, err error) {
	conn := ln.listener.Accept()
	return conn, nil
}
func (ln *smbnpListener) Addr() net.Addr {
	return &net.UnixAddr{Name: `\\.\pipe\`+ln.pipeName, Net: "smbnp"}
}
func (ln *smbnpListener) Close() error {
	return nil
}

type pipeListener struct {
	name string
	path string
	handle windows.Handle
	connChan chan net.Conn
	closeChan chan struct{}
}

func PipeListener(name string, fn func()) (l *pipeListener) {
	ln := &pipeListener{
		name: name,
		path: `\\.\pipe\`,
		connChan: make(chan net.Conn),
		closeChan: make(chan struct{}),
	}
	go ln.listenLoop(fn)
	return ln
}

func (ln *pipeListener) Accept() (c net.Conn, err error) {
	select {
	case conn := <-ln.connChan:
		return conn, nil
	case <-ln.closeChan:
		return nil, errors.New("Pipe listener closed")
	}
}

func (ln *pipeListener) Close() error {
	close(ln.closeChan)
	return nil
}

func (ln *pipeListener) Addr() net.Addr {
	return &net.UnixAddr{Name: `\\.\pipe\`+ln.name, Net: "smbnp"}
}

func (ln *pipeListener) listenLoop(fn func()) {
	for {
		select {
		case <-ln.closeChan:
			conn := <-ln.connChan
			conn.Close()
			return
		default:
		}

		handle, err := windows.CreateNamedPipe(
			windows.StringToUTF16Ptr(ln.path + ln.name),
			windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
			windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
			windows.PIPE_UNLIMITED_INSTANCES,
			65536,
			65536,
			0,
			nil,
		)
		if err != nil {
			continue
		}
		if fn != nil {
			fn()
		}

		err = windows.ConnectNamedPipe(handle, nil)
		if err != nil && err != windows.ERROR_PIPE_CONNECTED {
			windows.CloseHandle(handle)
			continue
		}


		ln.connChan <- &PipeConn{handle, `\\.\pipe\`+ln.name}

		if fn != nil {
			break
		}
	}
}


type PipeConn struct{
	handle windows.Handle
	path string
}

func (c *PipeConn) Close() error {
	if c.handle == 0 || c.handle == windows.InvalidHandle {
		return nil
	}
	windows.DisconnectNamedPipe(c.handle)
	return windows.CloseHandle(c.handle)
}

func (c *PipeConn) Read(p []byte) (int, error) {
	var read uint32
	// var actual_read uint32

	// err := windows.ReadFile(c.handle, (*(*[4]byte)(unsafe.Pointer(&actual_read)))[:], &read, nil)
	// if err != nil {
	// 	return 0, err
	// }

	buf := make([]byte, 4096)

	err := windows.ReadFile(c.handle, buf, &read, nil)
	if err != nil {
		return 0, err
	}
	copy(p, buf[:read])

	return int(read), nil
}

func (c *PipeConn) Write(p []byte) (int, error) {
	var written uint32
	// to_write := uint32(len(p))

	// err := windows.WriteFile(c.handle, (*(*[4]byte)(unsafe.Pointer(&to_write)))[:], &written, nil)
	// if err != nil {
	// 	return 0, err
	// }

	err := windows.WriteFile(c.handle, p, &written, nil)
	if err != nil {
		return 0, err
	}
	return int(written), nil
}

func (c *PipeConn) LocalAddr() net.Addr  {
	return &net.UnixAddr{Name: c.path, Net: "smbnp"}
}
func (c *PipeConn) RemoteAddr() net.Addr {
	return &net.UnixAddr{Name: c.path, Net: "smbnp"}
}

func (*PipeConn) SetDeadline(t time.Time) error      { return nil }
func (*PipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (*PipeConn) SetWriteDeadline(t time.Time) error { return nil }

func pipeDial(path string) (*PipeConn, error) {
	namePtr, _ := syscall.UTF16PtrFromString(path)
	handle, err := windows.CreateFile(
		namePtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		return nil, err
	}
	var mode uint32 = windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT
	err = windows.SetNamedPipeHandleState(handle, &mode, nil, nil)
	if err != nil {
		return nil, err
	}
	return &PipeConn{handle, path}, nil
}

type PipeConnDuplex struct {
	name string
	in *PipeConn
	out *PipeConn
}

func duplexPipeDial(path string, name string) (*PipeConnDuplex, error) {
	in_path := path + name + `.in`
	out_path := path + name + `.in`
	in_conn, err := pipeDial(in_path)
	if err != nil {
		return nil, err
	}
	// time.Sleep(1 * time.Second)
	out_conn, err := pipeDial(out_path)
	if err != nil {
		return nil, err
	}
	return &PipeConnDuplex{name, in_conn, out_conn}, nil
}

func (c *PipeConnDuplex) Close() error {
	c.in.Close()
	c.out.Close()
	return nil
}

func (c *PipeConnDuplex) Read(p []byte) (int, error) {
	return c.in.Read(p)
}

func (c *PipeConnDuplex) Write(p []byte) (int, error) {
	return c.out.Write(p)
}

func (c *PipeConnDuplex) LocalAddr() net.Addr  {
	return &net.UnixAddr{Name: c.in.path, Net: "smbnp"}
}
func (c *PipeConnDuplex) RemoteAddr() net.Addr {
	return &net.UnixAddr{Name: c.in.path, Net: "smbnp"}
}

func (*PipeConnDuplex) SetDeadline(t time.Time) error      { return nil }
func (*PipeConnDuplex) SetReadDeadline(t time.Time) error  { return nil }
func (*PipeConnDuplex) SetWriteDeadline(t time.Time) error { return nil }

type PipeDuplex struct {
	name string
	in *pipeListener
}

func CreatePipeDuplex(name string, fn func()) (*PipeDuplex, error) {
	in_path := name + `.in`
	// out_path := name + `.in`
	// c := make(chan struct{}, 2)
	var in_listener *pipeListener
	// var out_listener *pipeListener
	in_listener = PipeListener(in_path, nil)
	// go func() {
	// 	out_listener = PipeListener(out_path, func() { c <- struct{}{}; })
	// }()
	// _ = <-c
	// _ = <-c
	fn()
	return &PipeDuplex{name, in_listener}, nil
}

func (pd *PipeDuplex) Accept() (*PipeConnDuplex, error) {
	in_conn, err := pd.in.Accept()
	if err != nil {
		return nil, err
	}
	out_conn, err := pd.in.Accept()
	if err != nil {
		return nil, err
	}
	in_conn_pipe, _ := in_conn.(*PipeConn)
	out_conn_pipe, _ := out_conn.(*PipeConn)
	return &PipeConnDuplex{pd.name, in_conn_pipe, out_conn_pipe}, nil
}

type PipeDispatcher struct {
	listener *pipeListener
	chans map[string] chan *PipeConn
	initc chan string
}

func createPipeDispatcher(path string) (*PipeDispatcher) {
	listener := PipeListener(path, nil)
	pd := &PipeDispatcher{listener, make(map[string] chan *PipeConn), make(chan string, 1024)}
	go pd.listenLoop()
	return pd
}

func (pd *PipeDispatcher) listenLoop() {
	for {
		conn, err := pd.listener.Accept()
		if err != nil {
			continue
		}
		cmd := make([]byte, 100)
		l, err := conn.Read(cmd)
		if err != nil {
			conn.Close()
			continue
		}
		if l == 1 { // CONNECT
			b := make([]byte, 4)
			rand.Read(b)
			conn_id := hex.EncodeToString(b)
			pd.chans[conn_id] = make(chan *PipeConn, 10)
			pd.initc <- conn_id
			pd.chans[conn_id] <- conn.(*PipeConn)
			conn.Write([]byte(conn_id))
		} else { // SECOND
			conn_id := string(cmd[:l])
			pd.chans[conn_id] <- conn.(*PipeConn)
			conn.Write([]byte("0"))
			delete(pd.chans, conn_id)
		}
	}
}

func (pd *PipeDispatcher) Accept() (*PipeConnDuplex) {
	conn_id := <- pd.initc
	conn1 := <- pd.chans[conn_id]
	conn2 := <- pd.chans[conn_id]
	return &PipeConnDuplex{pd.listener.path, conn1, conn2}
}

type ConnDispatcher struct {
	listener *PipeDispatcher
	connCh chan *yamux.Stream
}

func (cd *ConnDispatcher) listenLoop() {
	for {
		conn := cd.listener.Accept()
		session, err := yamux.Server(conn, nil)
		if err != nil {
			continue
		}
		go func() {
			for {
				stream, err := session.Accept()
				if err != nil {
					break
				}
				cd.connCh <- stream.(*yamux.Stream)
			}
		}()
	}
}

func (cd *ConnDispatcher) Accept() (*yamux.Stream) {
	conn := <- cd.connCh
	return conn
}