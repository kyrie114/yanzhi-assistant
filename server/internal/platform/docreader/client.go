package docreader

import (
	"context"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"yanzhi/server/internal/apperr"
	pb "yanzhi/server/internal/platform/docreader/pb"
)

// Block 是一页里的一段正文。Kind 取 paragraph、table、heading 或 figure。
type Block struct {
	Kind string
	Text string
}

// Page 是 docreader 交回的一页。PageNo 是物理页，从 1 开始。
type Page struct {
	PageNo int
	Blocks []Block
}

type Client struct {
	addr    string
	token   string
	timeout time.Duration
}

func New(addr, token string, timeout time.Duration) *Client {
	return &Client{addr: addr, token: token, timeout: timeout}
}

type Result struct {
	PageCount         int
	HasPhysicalPages  bool
	Pages             []Page
	Skipped           []int
	ErrCode           string
}

func (c *Client) ParsePDF(ctx context.Context, documentID, requestID string, data []byte) (Result, error) {
	if c == nil || c.addr == "" {
		return Result{}, apperr.ErrDocreaderDown
	}
	dialCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, c.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(64<<20),
			grpc.MaxCallSendMsgSize(64<<20),
		),
	)
	if err != nil {
		return Result{}, apperr.ErrDocreaderDown
	}
	defer conn.Close()
	callCtx := metadata.AppendToOutgoingContext(dialCtx, "x-docreader-token", c.token)
	stream, err := pb.NewDocReaderClient(conn).Parse(callCtx)
	if err != nil {
		return Result{}, down(err)
	}
	header := &pb.ParseRequest{Part: &pb.ParseRequest_Header{Header: &pb.ParseHeader{
		RequestId: requestID, DocumentId: documentID, SourceType: pb.SourceType_PDF,
		FileName: "upload.pdf", SizeBytes: int64(len(data)), MaxPages: 300,
	}}}
	if err := stream.Send(header); err != nil {
		return Result{}, down(err)
	}
	for off := 0; off < len(data); off += 1 << 20 {
		end := off + (1 << 20)
		if end > len(data) {
			end = len(data)
		}
		if err := stream.Send(&pb.ParseRequest{Part: &pb.ParseRequest_Data{Data: data[off:end]}}); err != nil {
			return Result{}, down(err)
		}
	}
	if err := stream.CloseSend(); err != nil {
		return Result{}, down(err)
	}
	var result Result
	result.HasPhysicalPages = true
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Result{}, down(err)
		}
		if info := ev.GetInfo(); info != nil {
			result.PageCount = int(info.GetPageCount())
			result.HasPhysicalPages = info.GetHasPhysicalPages()
		}
		if page := ev.GetPage(); page != nil {
			p := Page{PageNo: int(page.GetPageNo())}
			if page.GetHasText() {
				for _, b := range page.GetBlocks() {
					kind := "paragraph"
					switch b.GetKind() {
					case pb.Block_TABLE:
						kind = "table"
					case pb.Block_HEADING:
						kind = "heading"
					case pb.Block_FIGURE:
						kind = "figure"
					}
					p.Blocks = append(p.Blocks, Block{Kind: kind, Text: b.GetText()})
				}
			} else {
				result.Skipped = append(result.Skipped, int(page.GetPageNo()))
			}
			result.Pages = append(result.Pages, p)
		}
		if pe := ev.GetError(); pe != nil {
			switch pe.GetCode() {
			case pb.ParseError_ENCRYPTED:
				result.ErrCode = "ENCRYPTED"
			case pb.ParseError_TOO_MANY_PAGES:
				result.ErrCode = "TOO_MANY_PAGES"
			default:
				result.ErrCode = "CORRUPTED"
			}
		}
	}
	return result, nil
}

func (c *Client) Healthy(ctx context.Context) bool {
	if c == nil || c.addr == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, c.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return false
	}
	defer conn.Close()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err == nil && resp.GetStatus() == healthpb.HealthCheckResponse_SERVING
}

func down(err error) error {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Unauthenticated:
			return apperr.ErrDocreaderDown
		}
	}
	if err == nil {
		return nil
	}
	return apperr.ErrDocreaderDown
}
