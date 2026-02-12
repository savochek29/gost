package gost

import (
	"net"
	// "fmt"
	"syscall"
	"strings"
	// "io"
	"time"
	"errors"
	"unsafe"
	// "github.com/hashicorp/yamux"
	"golang.org/x/sys/windows"
    "crypto/rand"
    "encoding/hex"
)

type smbnpTransporter struct{
	pipeName string
	path string

}

func SMBNPTransporter(url string) Transporter {
	parts := strings.SplitN(url, "/", 4)
	pipeName := parts[len(parts)-1]
	host := strings.Split(parts[2], ":")[0]
	path := `\\` + host + `\pipe\`

	return &smbnpTransporter{pipeName, path}
}

func (tr *smbnpTransporter) Dial(addr string, options ...DialOption) (net.Conn, error) {

	conn, err := pipeDial(tr.path + tr.pipeName)
	if err != nil {
		return nil, err
	}

	newNameB := make([]byte, 100)
	length, err := conn.Read(newNameB)
	if err != nil {
		return nil, err
	}
	new_name := string(newNameB[:length])

	conn_duplex, err := duplexPipeDial(tr.path, new_name)
	if err != nil {
		return nil, err
	}

	return conn_duplex, nil
}

func (tr *smbnpTransporter) Handshake(conn net.Conn, options ...HandshakeOption) (net.Conn, error) {
	return conn, nil
}

func (tr *smbnpTransporter) Multiplex() bool {
	return false
}

type smbnpListener struct {
	listener *pipeListener
	pipeName string
}

func SMBNPListener(pipeName string) (Listener, error) {

	listener := PipeListener(pipeName, nil)

	return &smbnpListener{listener, pipeName}, nil
}

func (ln *smbnpListener) Accept() (c net.Conn, err error) {
	conn, err := ln.listener.Accept()
	if err != nil {
		return nil, err
	}

	b := make([]byte, 4)
	rand.Read(b)
	new_name := hex.EncodeToString(b)

	pd, err := CreatePipeDuplex(new_name, func() { conn.Write([]byte(new_name)) })
	if err != nil {
		return nil, err
	}
	
	dupconn, err := pd.Accept()
	if err != nil {
		return nil, err
	}

	conn.Close()

	return dupconn, nil
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
			windows.PIPE_ACCESS_DUPLEX,
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
	var actual_read uint32

	err := windows.ReadFile(c.handle, (*(*[4]byte)(unsafe.Pointer(&actual_read)))[:], &read, nil)
	if err != nil {
		return 0, err
	}

	buf := make([]byte, actual_read)

	err = windows.ReadFile(c.handle, buf, &read, nil)
	if err != nil {
		return 0, err
	}
	copy(p, buf)

	return int(read), nil
}

func (c *PipeConn) Write(p []byte) (int, error) {
	var written uint32
	to_write := uint32(len(p))

	err := windows.WriteFile(c.handle, (*(*[4]byte)(unsafe.Pointer(&to_write)))[:], &written, nil)
	if err != nil {
		return 0, err
	}

	err = windows.WriteFile(c.handle, p, &written, nil)
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
	in_path := path + name + `.out`
	out_path := path + name + `.in`
	in_conn, err := pipeDial(in_path)
	if err != nil {
		return nil, err
	}
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
	return &net.UnixAddr{Name: c.out.path, Net: "smbnp"}
}

func (*PipeConnDuplex) SetDeadline(t time.Time) error      { return nil }
func (*PipeConnDuplex) SetReadDeadline(t time.Time) error  { return nil }
func (*PipeConnDuplex) SetWriteDeadline(t time.Time) error { return nil }

type PipeDuplex struct {
	name string
	in *pipeListener
	out *pipeListener
}

func CreatePipeDuplex(name string, fn func()) (*PipeDuplex, error) {
	in_path := name + `.in`
	out_path := name + `.out`
	c := make(chan struct{}, 2)
	var in_listener *pipeListener
	var out_listener *pipeListener
	go func() {
		in_listener = PipeListener(in_path, func() { c <- struct{}{}; })
	}()
	go func() {
		out_listener = PipeListener(out_path, func() { c <- struct{}{}; })
	}()
	_ = <-c
	_ = <-c
	fn()
	return &PipeDuplex{name, in_listener, out_listener}, nil
}

func (pd *PipeDuplex) Accept() (*PipeConnDuplex, error) {
	in_conn, err := pd.in.Accept()
	if err != nil {
		return nil, err
	}
	out_conn, err := pd.out.Accept()
	if err != nil {
		return nil, err
	}
	in_conn_pipe, _ := in_conn.(*PipeConn)
	out_conn_pipe, _ := out_conn.(*PipeConn)
	return &PipeConnDuplex{pd.name, in_conn_pipe, out_conn_pipe}, nil
}