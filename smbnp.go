package gost

import (
	"net"
	"fmt"
	"syscall"
	"strings"
	// "github.com/hashicorp/yamux"
	"golang.org/x/sys/windows"
)

type smbnpTransporter struct{
	pipeName string
}

func SMBNPTransporter(url string) Transporter {
	parts := strings.SplitN(url, "/", 4)
	fmt.Println(parts[3])
	return &smbnpTransporter{parts[3]}
}

func (tr *smbnpTransporter) Dial(addr string, options ...DialOption) (net.Conn, error) {

	fmt.Println("Dial")

	opts := &DialOptions{}
	for _, option := range options {
		option(opts)
	}

	handle, err := OpenPipe(`\\` + addr + `\pipe\` + tr.pipeName)
	if err != nil {
		return nil, err
	}

	return &PipeConn{nil, handle}, nil
}

func (tr *smbnpTransporter) Handshake(conn net.Conn, options ...HandshakeOption) (net.Conn, error) {
	return conn, nil
}

func (tr *smbnpTransporter) Multiplex() bool {
	return false
}

type smbnpListener struct {
	pipeConn PipeConn
}

func SMBNPListener(pipeName string) (Listener, error) {

	handle, err := CreateOrOpenPipe(pipeName)
	if err != nil {
		return nil, err
	}
	
	return &smbnpListener{pipeConn: PipeConn{nil, handle}}, nil
}

func (ln smbnpListener) Accept() (c net.Conn, err error) {
	return &ln.pipeConn, nil
}
func (ln smbnpListener) Addr() net.Addr {
	return nil
}
func (ln smbnpListener) Close() error {
	return nil
}

func OpenPipe(path string) (windows.Handle, error) {
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
		return 0, err
	}
	return handle, nil
}
func CreateOrOpenPipe(pipeName string) (windows.Handle, error) {
	fullPath := `\\.\pipe\` + pipeName

	handle, err := OpenPipe(fullPath)

	if err == syscall.ERROR_FILE_NOT_FOUND {
		return CreateNamedPipe(pipeName)
	}

	return handle, nil
}

func CreateNamedPipe(pipeName string) (windows.Handle, error) {
	fullPath := `\\.\pipe\` + pipeName
	namePtr, _ := syscall.UTF16PtrFromString(fullPath)

	handle, err := windows.CreateNamedPipe(
		namePtr,
		windows.PIPE_ACCESS_DUPLEX, // чтение/запись
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE, // режим байтов
		windows.PIPE_UNLIMITED_INSTANCES,
		65536,
		65536,
		0,
		nil,
	)
	if err != nil {
		return 0, fmt.Errorf("ошибка создания пайпа: %v", err)
	}

	fmt.Println("Ожидание подключения клиента...")
	err = windows.ConnectNamedPipe(handle, nil)
	if err != nil && err != windows.ERROR_PIPE_CONNECTED {
		windows.CloseHandle(handle)
		return 0, fmt.Errorf("ConnectNamedPipe failed: %v", err)
	}

	fmt.Println("Клиент подключился")
	return handle, nil
}

type PipeConn struct {
	net.Conn
    handle windows.Handle
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
	err := windows.ReadFile(
		c.handle,
		p,
		&read,
		nil,
	)
	if err != nil {
		return 0, err
	}
	return int(read), nil
}

func (c *PipeConn) Write(p []byte) (int, error) {
	var written uint32
	err := windows.WriteFile(
		c.handle,
		p,
		&written,
		nil,
	)
	if err != nil {
		return 0, err
	}
	return int(written), nil
}