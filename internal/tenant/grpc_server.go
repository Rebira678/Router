package tenant

import (
	"context"
	"time"

	"github.com/golang-jwt/jwt/v5"
	pb "github/rebik/pkg/api/proto/router/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GRPCServer struct {
	pb.UnimplementedTenantServiceServer
	jwtSecret string
}

func NewGRPCServer(secret string) *GRPCServer {
	return &GRPCServer{jwtSecret: secret}
}

func (s *GRPCServer) CreateTenant(ctx context.Context, req *pb.CreateTenantRequest) (*pb.CreateTenantResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	claims := jwt.MapClaims{
		"sub": req.TenantId,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(30 * 24 * time.Hour).Unix(), // Issue 30-day tokens
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to sign token: %v", err)
	}

	return &pb.CreateTenantResponse{
		JwtToken: signed,
	}, nil
}

func (s *GRPCServer) RevokeTenant(ctx context.Context, req *pb.RevokeTenantRequest) (*pb.RevokeTenantResponse, error) {
	// Day 13 scope: just a stub. In a real system, this would write to a Redis
	// blocklist or update a Postgres 'active' column.
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	
	// Mock successful revocation
	return &pb.RevokeTenantResponse{
		Success: true,
	}, nil
}
