package dispatcher

import (
	"context"
	"fmt"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

// Both dispatcher entry points must keep counters and online half-close tracking
// in the data path, even for users whose speed limit returns a nil bucket.
func TestManagedTrafficAccountsBeforeClose(t *testing.T) {
	limiter.Init()
	for _, entry := range []string{"pipe", "direct"} {
		for _, speed := range []int{0, 100} {
			t.Run(fmt.Sprintf("%s/speed-%d", entry, speed), func(t *testing.T) {
				tag := fmt.Sprintf("accounting-%s-%d", entry, speed)
				email := tag + "|u"
				l := limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 42, Uuid: "u", SpeedLimit: speed}}, nil)
				defer limiter.DeleteLimiter(tag)
				in := &session.Inbound{
					Tag: tag, User: &protocol.MemoryUser{Email: email}, CanSpliceCopy: 2,
					Source: net.TCPDestination(net.ParseAddress("198.51.100.1"), 12345),
				}
				ctx, cancel := context.WithCancel(session.ContextWithInbound(context.Background(), in))
				defer cancel()
				ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{}})
				destination := net.TCPDestination(net.ParseAddress("example.com"), 443)
				d := &DefaultDispatcher{}
				var upload buf.Writer
				var download buf.Reader
				h := &testHandler{during: func(link *transport.Link) {
					if (speed == 0 && in.CanSpliceCopy != 2) || (speed > 0 && in.CanSpliceCopy != 3) {
						t.Fatal("splice eligibility must preserve unlimited users and enforce rate limits")
					}
					if provider, ok := link.Writer.(interface{ SpliceCounter() stats.Counter }); !ok || provider.SpliceCounter() == nil {
						t.Fatal("online wrapper hid the splice counter")
					}
					ts := counter.LoadOrCreate(&d.Counter, tag).GetCounter(email)
					for phase := 0; phase < 2; phase++ {
						if err := upload.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes([]byte("up!"))}); err != nil {
							t.Fatal(err)
						}
						mb, err := link.Reader.ReadMultiBuffer()
						buf.ReleaseMulti(mb)
						if err != nil {
							t.Fatal(err)
						}
						if err := link.Writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes([]byte("down!"))}); err != nil {
							t.Fatal(err)
						}
						mb, err = download.ReadMultiBuffer()
						buf.ReleaseMulti(mb)
						if err != nil {
							t.Fatal(err)
						}
						if up, down := ts.Read(true); up != 3 || down != 5 {
							t.Fatalf("phase %d live drain: up=%d down=%d", phase, up, down)
						}
						if up, down := ts.Read(false); up != 0 || down != 0 {
							t.Fatalf("drained bytes counted again: up=%d down=%d", up, down)
						}
					}
					if len(l.ActiveDeviceSnapshot()[42]) != 1 {
						t.Fatal("live transfer is not tracked")
					}
					common.Close(link.Writer)
					if len(l.ActiveDeviceSnapshot()[42]) != 1 {
						t.Fatal("half-close released a live device")
					}
					common.Interrupt(link.Reader)
					if len(l.ActiveDeviceSnapshot()) != 0 {
						t.Fatal("completed transfer retained its device")
					}
				}}
				d.ohm = &testManager{handler: h}
				if entry == "pipe" {
					inbound, outbound, limit, err := d.getLink(ctx, net.Network_TCP)
					if err != nil {
						t.Fatal(err)
					}
					upload, download = inbound.Writer, inbound.Reader
					defer common.Close(upload)
					defer common.Interrupt(download)
					d.routedDispatch(ctx, outbound, destination, limit, "")
				} else {
					upReader, upWriter := pipe.New()
					downReader, downWriter := pipe.New()
					upload, download = upWriter, downReader
					defer common.Close(upWriter)
					defer common.Interrupt(downReader)
					if err := d.DispatchLink(ctx, destination, &transport.Link{Reader: upReader, Writer: downWriter}); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
