package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	pb "github/rebik/pkg/api/proto/router/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: go run ./cmd/grpcclient <tenant-id>")
	}
	tenantID := os.Args[1]

	// 1. Connect to the internal gRPC admin server on port 9092
	// We use Insecure credentials because this is an internal, firewalled API
	conn, err := grpc.NewClient("localhost:9092", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Did not connect: %v", err)
	}
	defer conn.Close()

	// 2. Initialize the generated client stub
	client := pb.NewTenantServiceClient(conn)

	// 3. Make the CreateTenant RPC call
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	fmt.Printf("Calling gRPC CreateTenant for: %s...\n", tenantID)
	resp, err := client.CreateTenant(ctx, &pb.CreateTenantRequest{
		TenantId: tenantID,
	})
	if err != nil {
		log.Fatalf("Could not create tenant: %v", err)
	}

	// 4. Print the result
	fmt.Printf("\n✅ Success! gRPC Server returned JWT Token:\n%s\n", resp.GetJwtToken())
}
