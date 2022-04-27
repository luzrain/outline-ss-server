package net

import (
	"encoding/binary"
	"hash/fnv"
	"io"
	"log"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// DuplexConn is a net.Conn that allows for closing only the reader or writer end of
// it, supporting half-open state.
type DuplexConn interface {
	net.Conn
	// Closes the Read end of the connection, allowing for the release of resources.
	// No more reads should happen.
	CloseRead() error
	// Closes the Write end of the connection. An EOF or FIN signal may be
	// sent to the connection target.
	CloseWrite() error
}

type duplexConnAdaptor struct {
	DuplexConn
	r io.Reader
	w io.Writer
}

func (dc *duplexConnAdaptor) Read(b []byte) (int, error) {
	return dc.r.Read(b)
}
func (dc *duplexConnAdaptor) WriteTo(w io.Writer) (int64, error) {
	return io.Copy(w, dc.r)
}
func (dc *duplexConnAdaptor) CloseRead() error {
	return dc.DuplexConn.CloseRead()
}
func (dc *duplexConnAdaptor) Write(b []byte) (int, error) {
	return dc.w.Write(b)
}
func (dc *duplexConnAdaptor) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(dc.w, r)
}
func (dc *duplexConnAdaptor) CloseWrite() error {
	return dc.DuplexConn.CloseWrite()
}

// WrapDuplexConn wraps an existing DuplexConn with new Reader and Writer, but
// preserving the original CloseRead() and CloseWrite().
func WrapConn(c DuplexConn, r io.Reader, w io.Writer) DuplexConn {
	conn := c
	// We special-case duplexConnAdaptor to avoid multiple levels of nesting.
	if a, ok := c.(*duplexConnAdaptor); ok {
		conn = a.DuplexConn
	}
	return &duplexConnAdaptor{DuplexConn: conn, r: r, w: w}
}

func copyOneWay(leftConn, rightConn DuplexConn) (int64, error) {
	n, err := io.Copy(leftConn, rightConn)
	// Send FIN to indicate EOF
	leftConn.CloseWrite()
	// Release reader resources
	rightConn.CloseRead()
	return n, err
}

// Relay copies between left and right bidirectionally. Returns number of
// bytes copied from right to left, from left to right, and any error occurred.
// Relay allows for half-closed connections: if one side is done writing, it can
// still read all remaining data from its peer.
func Relay(leftConn, rightConn DuplexConn) (int64, int64, error) {
	type res struct {
		N   int64
		Err error
	}
	ch := make(chan res)

	go func() {
		n, err := copyOneWay(rightConn, leftConn)
		ch <- res{n, err}
	}()

	n, err := copyOneWay(leftConn, rightConn)
	rs := <-ch

	if err == nil {
		err = rs.Err
	}
	return n, rs.N, err
}

type ConnectionError struct {
	// TODO: create status enums and move to metrics.go
	Status  string
	Message string
	Cause   error
}

func NewConnectionError(status, message string, cause error) *ConnectionError {
	return &ConnectionError{Status: status, Message: message, Cause: cause}
}

type Marker struct {
	mark int
}

func NewMarker(clientIP net.IP) Marker {
	if clientIP.To4() != nil {
		// Place the client IP directly into the mark
		return Marker{int(binary.BigEndian.Uint32(clientIP))}
	} else {
		// Hash the top 48 bits of client IP into the mark.
		// Each IPv6 customer can receive up to a /48 from their ISP, e.g.
		// https://www.ripe.net/publications/docs/ripe-690#4-2--prefix-assignment-options
		top48 := clientIP[:6]
		hash := fnv.New32a()
		hash.Write(top48)
		return Marker{int(hash.Sum32())}
	}
}

func (m Marker) MarkSocket(conn syscall.Conn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	return raw.Control(func(fd uintptr) {
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, syscall.SO_MARK, m.mark); err != nil {
			log.Printf("setsockopt: %v", err)
		}
	})
}
