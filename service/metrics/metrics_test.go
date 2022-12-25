package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMethodsDontPanic(t *testing.T) {
	ssMetrics := NewPrometheusShadowsocksMetrics(prometheus.NewPedanticRegistry())
	proxyMetrics := ProxyMetrics{
		ClientProxy: 1,
		ProxyTarget: 2,
		TargetProxy: 3,
		ProxyClient: 4,
	}
	ssMetrics.SetNumAccessKeys(20, 2)
	ssMetrics.AddOpenTCPConnection("127.0.0.1")
	ssMetrics.AddClosedTCPConnection("127.0.0.1", "1", "OK", proxyMetrics, 10*time.Millisecond, 100*time.Millisecond)
	ssMetrics.AddTCPProbe("ERR_CIPHER", "eof", 443, proxyMetrics)
	ssMetrics.AddUDPPacketFromClient("127.0.0.1", "2", "OK", 10, 20, 10*time.Millisecond)
	ssMetrics.AddUDPPacketFromTarget("127.0.0.1", "3", "OK", 10, 20)
	ssMetrics.AddUDPNatEntry()
	ssMetrics.RemoveUDPNatEntry()
}

func BenchmarkOpenTCP(b *testing.B) {
	ssMetrics := NewPrometheusShadowsocksMetrics(prometheus.NewRegistry())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ssMetrics.AddOpenTCPConnection("127.0.0.1", "1")
	}
}

func BenchmarkCloseTCP(b *testing.B) {
	ssMetrics := NewPrometheusShadowsocksMetrics(prometheus.NewRegistry())
	clientIp := "127.0.0.1"
	accessKey := "key 1"
	status := "OK"
	data := ProxyMetrics{}
	timeToCipher := time.Microsecond
	duration := time.Minute
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ssMetrics.AddClosedTCPConnection(clientIp, accessKey, status, data, timeToCipher, duration)
	}
}

func BenchmarkProbe(b *testing.B) {
	ssMetrics := NewPrometheusShadowsocksMetrics(nil, prometheus.NewRegistry())
	status := "ERR_REPLAY"
	drainResult := "other"
	port := 12345
	data := ProxyMetrics{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ssMetrics.AddTCPProbe(status, drainResult, port, data)
	}
}

func BenchmarkClientUDP(b *testing.B) {
	ssMetrics := NewPrometheusShadowsocksMetrics(prometheus.NewRegistry())
	clientIp := "127.0.0.1"
	accessKey := "key 1"
	status := "OK"
	size := 1000
	timeToCipher := time.Microsecond
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ssMetrics.AddUDPPacketFromClient(clientIp, accessKey, status, size, size, timeToCipher)
	}
}

func BenchmarkTargetUDP(b *testing.B) {
	ssMetrics := NewPrometheusShadowsocksMetrics(prometheus.NewRegistry())
	clientIp := "127.0.0.1"
	accessKey := "key 1"
	status := "OK"
	size := 1000
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ssMetrics.AddUDPPacketFromTarget(clientIp, accessKey, status, size, size)
	}
}

func BenchmarkNAT(b *testing.B) {
	ssMetrics := NewPrometheusShadowsocksMetrics(prometheus.NewRegistry())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ssMetrics.AddUDPNatEntry()
		ssMetrics.RemoveUDPNatEntry()
	}
}
