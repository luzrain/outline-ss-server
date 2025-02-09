// Copyright 2018 Jigsaw Operations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metrics

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	onet "github.com/Jigsaw-Code/outline-ss-server/net"
	"github.com/prometheus/client_golang/prometheus"
)

// ShadowsocksMetrics registers metrics for the Shadowsocks service.
type ShadowsocksMetrics interface {
	SetBuildInfo(version string)

	GetIpAddress(net.Addr) string

	SetNumAccessKeys(numKeys int, numPorts int)

	// TCP metrics
	AddOpenTCPConnection(clientIp, accessKey string)
	AddClosedTCPConnection(clientIp, accessKey, status string, data ProxyMetrics, timeToCipher, duration time.Duration)
	AddTCPProbe(status, drainResult string, port int, data ProxyMetrics)

	// UDP metrics
	AddUDPPacketFromClient(clientIp, accessKey, status string, clientProxyBytes, proxyTargetBytes int, timeToCipher time.Duration)
	AddUDPPacketFromTarget(clientIp, accessKey, status string, targetProxyBytes, proxyClientBytes int)
	AddUDPNatEntry()
	RemoveUDPNatEntry()
}

type shadowsocksMetrics struct {
	buildInfo      *prometheus.GaugeVec
	accessKeys     prometheus.Gauge
	ports          prometheus.Gauge
	dataBytes      *prometheus.CounterVec
	timeToCipherMs *prometheus.HistogramVec
	// TODO: Add time to first byte.

	tcpProbes               *prometheus.HistogramVec
	tcpOpenConnections      *prometheus.CounterVec
	tcpClosedConnections    *prometheus.CounterVec
	tcpConnectionDurationMs *prometheus.HistogramVec

	udpAddedNatEntries   prometheus.Counter
	udpRemovedNatEntries prometheus.Counter
}

func newShadowsocksMetrics() *shadowsocksMetrics {
	// Don't forget to pass the counters to the registerer.MustRegister call in NewPrometheusShadowsocksMetrics.
	return &shadowsocksMetrics{
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "shadowsocks",
			Name:      "build_info",
			Help:      "Information on the outline-ss-server build",
		}, []string{"version"}),
		accessKeys: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "shadowsocks",
			Name:      "keys",
			Help:      "Count of access keys",
		}),
		ports: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "shadowsocks",
			Name:      "ports",
			Help:      "Count of open Shadowsocks ports",
		}),
		tcpProbes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "shadowsocks",
			Name:      "tcp_probes",
			Buckets:   []float64{0, 49, 50, 51, 73, 91},
			Help:      "Histogram of number of bytes from client to proxy, for detecting possible probes",
		}, []string{"port", "status", "error"}),
		tcpOpenConnections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "shadowsocks",
			Subsystem: "tcp",
			Name:      "connections_opened",
			Help:      "Count of open TCP connections",
		}, []string{"ip", "access_key"}),
		tcpClosedConnections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "shadowsocks",
			Subsystem: "tcp",
			Name:      "connections_closed",
			Help:      "Count of closed TCP connections",
		}, []string{"ip", "status", "access_key"}),
		tcpConnectionDurationMs: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "shadowsocks",
				Subsystem: "tcp",
				Name:      "connection_duration_ms",
				Help:      "TCP connection duration distributions.",
				Buckets: []float64{
					100,
					float64(time.Second.Milliseconds()),
					float64(time.Minute.Milliseconds()),
					float64(time.Hour.Milliseconds()),
					float64(24 * time.Hour.Milliseconds()),     // Day
					float64(7 * 24 * time.Hour.Milliseconds()), // Week
				},
			}, []string{"status"}),
		dataBytes: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "shadowsocks",
				Name:      "data_bytes",
				Help:      "Bytes transferred by the proxy, per access key and ip",
			}, []string{"dir", "proto", "access_key", "ip"}),
		timeToCipherMs: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "shadowsocks",
				Name:      "time_to_cipher_ms",
				Help:      "Time needed to find the cipher",
				Buckets:   []float64{0.1, 1, 10, 100, 1000},
			}, []string{"proto", "found_key"}),
		udpAddedNatEntries: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: "shadowsocks",
				Subsystem: "udp",
				Name:      "nat_entries_added",
				Help:      "Entries added to the UDP NAT table",
			}),
		udpRemovedNatEntries: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: "shadowsocks",
				Subsystem: "udp",
				Name:      "nat_entries_removed",
				Help:      "Entries removed from the UDP NAT table",
			}),
	}
}

// NewPrometheusShadowsocksMetrics constructs a metrics object that uses
func NewPrometheusShadowsocksMetrics(registerer prometheus.Registerer) ShadowsocksMetrics {
	m := newShadowsocksMetrics()
	// TODO: Is it possible to pass where to register the collectors?
	registerer.MustRegister(m.buildInfo, m.accessKeys, m.ports, m.tcpProbes, m.tcpOpenConnections, m.tcpClosedConnections, m.tcpConnectionDurationMs,
		m.dataBytes, m.timeToCipherMs, m.udpAddedNatEntries, m.udpRemovedNatEntries)
	return m
}

func (m *shadowsocksMetrics) SetBuildInfo(version string) {
	m.buildInfo.WithLabelValues(version).Set(1)
}

func (m *shadowsocksMetrics) GetIpAddress(addr net.Addr) string {
	ip, _, _ := net.SplitHostPort(addr.String())
	return ip
}

func (m *shadowsocksMetrics) SetNumAccessKeys(numKeys int, ports int) {
	m.accessKeys.Set(float64(numKeys))
	m.ports.Set(float64(ports))
}

func (m *shadowsocksMetrics) AddOpenTCPConnection(clientIp, accessKey string) {
	m.tcpOpenConnections.WithLabelValues(clientIp, accessKey).Inc()
}

// Converts accessKey to "true" or "false"
func isFound(accessKey string) string {
	return fmt.Sprintf("%t", accessKey != "")
}

// addIfNonZero helps avoid the creation of series that are always zero.
func addIfNonZero(value int64, counterVec *prometheus.CounterVec, lvs ...string) {
	if value > 0 {
		counterVec.WithLabelValues(lvs...).Add(float64(value))
	}
}

func (m *shadowsocksMetrics) AddClosedTCPConnection(clientIp, accessKey, status string, data ProxyMetrics, timeToCipher, duration time.Duration) {
	m.tcpClosedConnections.WithLabelValues(clientIp, status, accessKey).Inc()
	m.tcpConnectionDurationMs.WithLabelValues(status).Observe(duration.Seconds() * 1000)
	m.timeToCipherMs.WithLabelValues("tcp", isFound(accessKey)).Observe(timeToCipher.Seconds() * 1000)
	addIfNonZero(data.ClientProxy, m.dataBytes, "c>p", "tcp", accessKey, clientIp)
	addIfNonZero(data.ProxyTarget, m.dataBytes, "p>t", "tcp", accessKey, clientIp)
	addIfNonZero(data.TargetProxy, m.dataBytes, "p<t", "tcp", accessKey, clientIp)
	addIfNonZero(data.ProxyClient, m.dataBytes, "c<p", "tcp", accessKey, clientIp)
}

func (m *shadowsocksMetrics) AddTCPProbe(status, drainResult string, port int, data ProxyMetrics) {
	m.tcpProbes.WithLabelValues(strconv.Itoa(port), status, drainResult).Observe(float64(data.ClientProxy))
}

func (m *shadowsocksMetrics) AddUDPPacketFromClient(clientIp, accessKey, status string, clientProxyBytes, proxyTargetBytes int, timeToCipher time.Duration) {
	m.timeToCipherMs.WithLabelValues("udp", isFound(accessKey)).Observe(timeToCipher.Seconds() * 1000)
	addIfNonZero(int64(clientProxyBytes), m.dataBytes, "c>p", "udp", accessKey, clientIp)
	addIfNonZero(int64(proxyTargetBytes), m.dataBytes, "p>t", "udp", accessKey, clientIp)
}

func (m *shadowsocksMetrics) AddUDPPacketFromTarget(clientIp, accessKey, status string, targetProxyBytes, proxyClientBytes int) {
	addIfNonZero(int64(targetProxyBytes), m.dataBytes, "p<t", "udp", accessKey, clientIp)
	addIfNonZero(int64(proxyClientBytes), m.dataBytes, "c<p", "udp", accessKey, clientIp)
}

func (m *shadowsocksMetrics) AddUDPNatEntry() {
	m.udpAddedNatEntries.Inc()
}

func (m *shadowsocksMetrics) RemoveUDPNatEntry() {
	m.udpRemovedNatEntries.Inc()
}

type ProxyMetrics struct {
	ClientProxy int64
	ProxyTarget int64
	TargetProxy int64
	ProxyClient int64
}

func (m *ProxyMetrics) add(other ProxyMetrics) {
	m.ClientProxy += other.ClientProxy
	m.ProxyTarget += other.ProxyTarget
	m.TargetProxy += other.TargetProxy
	m.ProxyClient += other.ProxyClient
}

type measuredConn struct {
	onet.DuplexConn
	io.WriterTo
	readCount *int64
	io.ReaderFrom
	writeCount *int64
}

func (c *measuredConn) Read(b []byte) (int, error) {
	n, err := c.DuplexConn.Read(b)
	*c.readCount += int64(n)
	return n, err
}

func (c *measuredConn) WriteTo(w io.Writer) (int64, error) {
	n, err := io.Copy(w, c.DuplexConn)
	*c.readCount += n
	return n, err
}

func (c *measuredConn) Write(b []byte) (int, error) {
	n, err := c.DuplexConn.Write(b)
	*c.writeCount += int64(n)
	return n, err
}

func (c *measuredConn) ReadFrom(r io.Reader) (int64, error) {
	n, err := io.Copy(c.DuplexConn, r)
	*c.writeCount += n
	return n, err
}

func MeasureConn(conn onet.DuplexConn, bytesSent, bytesReceived *int64) onet.DuplexConn {
	return &measuredConn{DuplexConn: conn, writeCount: bytesSent, readCount: bytesReceived}
}

// NoOpMetrics is a fake ShadowsocksMetrics that doesn't do anything. Useful in tests
// or if you don't want to track metrics.
type NoOpMetrics struct{}

func (m *NoOpMetrics) SetBuildInfo(version string) {}
func (m *NoOpMetrics) AddTCPProbe(status, drainResult string, port int, data ProxyMetrics) {
}
func (m *NoOpMetrics) AddClosedTCPConnection(clientIp, accessKey, status string, data ProxyMetrics, timeToCipher, duration time.Duration) {
}
func (m *NoOpMetrics) GetIpAddress(net.Addr) string {
	return ""
}
func (m *NoOpMetrics) SetNumAccessKeys(numKeys int, numPorts int)      {}
func (m *NoOpMetrics) AddOpenTCPConnection(clientIp, accessKey string) {}
func (m *NoOpMetrics) AddUDPPacketFromClient(clientIp, accessKey, status string, clientProxyBytes, proxyTargetBytes int, timeToCipher time.Duration) {
}
func (m *NoOpMetrics) AddUDPPacketFromTarget(clientIp, accessKey, status string, targetProxyBytes, proxyClientBytes int) {
}
func (m *NoOpMetrics) AddUDPNatEntry()    {}
func (m *NoOpMetrics) RemoveUDPNatEntry() {}
