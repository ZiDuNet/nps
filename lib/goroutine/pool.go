package goroutine

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
	"github.com/panjf2000/ants/v2"
)

type connGroup struct {
	src       io.ReadWriteCloser
	dst       io.ReadWriteCloser
	wg        *sync.WaitGroup
	n         *int64
	flow      *file.Flow
	task      *file.Tunnel
	host      *file.Host
	remote    string
	direction FlowDirection
}

// FlowDirection describes the direction from the public NPS listener's
// perspective. Inbound is public -> intranet and outbound is intranet ->
// public. FlowBoth is retained for callers that do not have an ownership
// direction; new proxy paths should use one of the directional values.
type FlowDirection uint8

const (
	FlowBoth FlowDirection = iota
	FlowInbound
	FlowOutbound
)

//func newConnGroup(dst, src io.ReadWriteCloser, wg *sync.WaitGroup, n *int64) connGroup {
//	return connGroup{
//		src: src,
//		dst: dst,
//		wg:  wg,
//		n:   n,
//	}
//}

func newConnGroup(dst, src io.ReadWriteCloser, wg *sync.WaitGroup, n *int64, flow *file.Flow, task *file.Tunnel, host *file.Host, remote string, direction FlowDirection) connGroup {
	return connGroup{
		src:       src,
		dst:       dst,
		wg:        wg,
		n:         n,
		flow:      flow,
		task:      task,
		host:      host,
		remote:    remote,
		direction: direction,
	}
}

func CopyBuffer(dst io.Writer, src io.Reader, flow *file.Flow, task *file.Tunnel, host *file.Host, remote string) (written int64, err error) {
	return CopyBufferWithFlows(dst, src, flow, nil, task, host, remote)
}

// CopyBufferWithFlows copies bytes while accounting them against the primary
// flow and any additional flows. The extra flow list is useful when one byte
// stream belongs to more than one ownership scope, such as a Host response
// that must count toward both the Host and its Client totals. This legacy
// entry point records bytes in both directions; proxy code should use
// CopyBufferWithFlowsDirection so inlet and export counters remain meaningful.
func CopyBufferWithFlows(dst io.Writer, src io.Reader, flow *file.Flow, additionalFlows []*file.Flow, task *file.Tunnel, host *file.Host, remote string) (written int64, err error) {
	return CopyBufferWithFlowsDirection(dst, src, flow, additionalFlows, task, host, remote, FlowBoth)
}

// CopyBufferWithFlowsDirection copies bytes without applying backpressure to
// the traffic inspector. Each ownership flow is updated exactly once per
// successful write, and quota checks are applied to every distinct flow.
func CopyBufferWithFlowsDirection(dst io.Writer, src io.Reader, flow *file.Flow, additionalFlows []*file.Flow, task *file.Tunnel, host *file.Host, remote string, direction FlowDirection) (written int64, err error) {
	buf := common.CopyBuff.Get()
	defer common.CopyBuff.Put(buf)
	var taskClient *file.Client
	var taskFlow *file.Flow
	if task != nil {
		task.RLock()
		taskClient, taskFlow = task.Client, task.Flow
		task.RUnlock()
	}
	var hostClient *file.Client
	var hostFlow *file.Flow
	if host != nil {
		host.RLock()
		hostClient, hostFlow = host.Client, host.Flow
		host.RUnlock()
	}
	for {
		if len(buf) <= 0 {
			break
		}
		nr, er := src.Read(buf)

		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
				trafficExceeded := false
				seenFlows := make([]*file.Flow, 0, 4+len(additionalFlows))
				recordFlow := func(candidate *file.Flow) {
					if candidate == nil {
						return
					}
					for _, seen := range seenFlows {
						if seen == candidate {
							return
						}
					}
					seenFlows = append(seenFlows, candidate)
					bytes := int64(nw)
					switch direction {
					case FlowInbound:
						candidate.Add(bytes, 0)
					case FlowOutbound:
						candidate.Add(0, bytes)
					default:
						candidate.Add(bytes, bytes)
					}
					trafficExceeded = trafficExceeded || candidate.Exceeded()
				}
				recordFlow(flow)
				recordFlow(taskFlow)
				recordFlow(hostFlow)
				for _, additionalFlow := range additionalFlows {
					recordFlow(additionalFlow)
				}
				if trafficExceeded {
					clientID := 0
					if taskClient != nil {
						taskClient.RLock()
						clientID = taskClient.Id
						taskClient.RUnlock()
					} else if hostClient != nil {
						hostClient.RLock()
						clientID = hostClient.Id
						hostClient.RUnlock()
					}
					logs.Error("客户端[%d]流量已经超出", clientID)
					err = errors.New("traffic exceeded")
				}
				if trafficExceeded {
					break
				}
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			err = er
			break
		}
	}
	return written, err
}

func copyConnGroup(group interface{}) {
	//logs.Info("copyConnGroup.........")
	cg, ok := group.(connGroup)
	if !ok {
		return
	}

	var err error
	written, err := CopyBufferWithFlowsDirection(cg.dst, cg.src, cg.flow, nil, cg.task, cg.host, cg.remote, cg.direction)
	if cg.n != nil {
		atomic.AddInt64(cg.n, written)
	}
	if err != nil {
		cg.src.Close()
		cg.dst.Close()
		//logs.Warn("close npc by copy from nps", err, c.connId)
	}

	//if conns.flow != nil {
	//	conns.flow.Add(in, out)
	//}
	cg.wg.Done()
}

type Conns struct {
	conn1 io.ReadWriteCloser // mux connection
	conn2 net.Conn           // outside connection
	flow  *file.Flow
	wg    *sync.WaitGroup
	task  *file.Tunnel
	host  *file.Host
	stats func(in, out int64)
}

func NewConns(c1 io.ReadWriteCloser, c2 net.Conn, flow *file.Flow, wg *sync.WaitGroup, task *file.Tunnel, host *file.Host) Conns {
	return NewConnsWithStats(c1, c2, flow, wg, task, host, nil)
}

// NewConnsWithStats reports the exact bytes copied in each direction after
// both workers exit. Bytes are counted after successful writes, so a peer that
// disconnects during a partial copy does not inflate the connection log.
func NewConnsWithStats(c1 io.ReadWriteCloser, c2 net.Conn, flow *file.Flow, wg *sync.WaitGroup, task *file.Tunnel, host *file.Host, stats func(in, out int64)) Conns {
	return Conns{
		conn1: c1,
		conn2: c2,
		flow:  flow,
		wg:    wg,
		task:  task,
		host:  host,
		stats: stats,
	}
}

func copyConns(group interface{}) {
	//logs.Info("copyConns.........")
	conns := group.(Conns)
	wg := new(sync.WaitGroup)
	wg.Add(2)
	var in, out int64
	remoteAddr := conns.conn2.RemoteAddr().String()
	_ = connCopyPool.Invoke(newConnGroup(conns.conn1, conns.conn2, wg, &in, conns.flow, conns.task, conns.host, remoteAddr, FlowInbound))
	// outside to mux : incoming
	_ = connCopyPool.Invoke(newConnGroup(conns.conn2, conns.conn1, wg, &out, conns.flow, conns.task, conns.host, remoteAddr, FlowOutbound))
	// mux to outside : outgoing
	wg.Wait()
	if conns.stats != nil {
		conns.stats(in, out)
	}
	//if conns.flow != nil {
	//	conns.flow.Add(in, out)
	//}
	conns.wg.Done()
}

var connCopyPool, _ = ants.NewPoolWithFunc(200000, copyConnGroup, ants.WithNonblocking(false))
var CopyConnsPool, _ = ants.NewPoolWithFunc(100000, copyConns, ants.WithNonblocking(false))
