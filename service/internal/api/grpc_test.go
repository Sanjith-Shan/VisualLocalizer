package api

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/Sanjith-Shan/VisualLocalizer/service/gen/vlocpb"
	"github.com/Sanjith-Shan/VisualLocalizer/service/internal/admit"
)

func TestGRPC(t *testing.T) {
	e := newEnv(t, 0, admit.Config{Workers: 1})
	e.put("heads", e.fakeMap("heads", 5))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	g := e.s.NewGRPC()
	go g.Serve(ln)
	defer g.Stop()
	cc, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	c := vlocpb.NewLocalizerClient(cc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	img := testImage(t, 640, 480, "jpeg", 1)
	k := &vlocpb.Intrinsics{Fx: 525, Fy: 525, Cx: 320, Cy: 240}
	r, err := c.Localize(ctx, &vlocpb.LocalizeRequest{Map: "heads", Image: img, Intrinsics: k})
	if err != nil || !r.Ok || r.Pose == nil || r.RequestId == "" {
		t.Fatalf("%v %v", r, err)
	}
	// Same pose as the HTTP API for the same frame.
	_, b := e.localize("heads", k7, img, nil)
	if want := `"w":` + ftoa(r.Pose.Qw); !contains(b, want) {
		t.Errorf("grpc pose %v not in http response %s", r.Pose, b)
	}
	for _, c2 := range []struct {
		req  *vlocpb.LocalizeRequest
		code codes.Code
	}{
		{&vlocpb.LocalizeRequest{Map: "nope", Image: img, Intrinsics: k}, codes.NotFound},
		{&vlocpb.LocalizeRequest{Map: "heads", Image: []byte("xx"), Intrinsics: k}, codes.InvalidArgument},
		{&vlocpb.LocalizeRequest{Map: "heads", Image: img}, codes.InvalidArgument},
		{&vlocpb.LocalizeRequest{Map: "heads", Image: img, Intrinsics: &vlocpb.Intrinsics{Fx: -1, Fy: 1, Cx: 1, Cy: 1}}, codes.InvalidArgument},
	} {
		if _, err := c.Localize(ctx, c2.req); status.Code(err) != c2.code {
			t.Errorf("got %v, want %v", err, c2.code)
		}
	}
	lm, err := c.ListMaps(ctx, &vlocpb.ListMapsRequest{})
	if err != nil || len(lm.Maps) != 1 || lm.Maps[0].NumPoints != 5 {
		t.Fatalf("%v %v", lm, err)
	}
}
