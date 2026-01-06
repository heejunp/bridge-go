package main

import (
	"context"
	"log"
	"net/http"

	"time"

	pb "bridge-go/proto"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	grpcClient pb.PodServiceClient
)

// Message format for Frontend
type BridgeMessage struct {
	Type     string    `json:"type"` // ADDED, MODIFIED, DELETED
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	HP       int       `json:"hp"`
	Position []float64 `json:"position"`
}

type IncomingMessage struct {
	Action string `json:"action"` // "kill"
	PodID  string `json:"podId"`
}

func main() {
	// 1. Connect to gRPC Server
	conn, err := grpc.Dial("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	grpcClient = pb.NewPodServiceClient(conn)

	// 2. Start WebSocket Server
	http.HandleFunc("/ws", handleWebSocket)
	
	log.Println("Bridge Server listening on :9090")
	if err := http.ListenAndServe(":9090", nil); err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}
	defer ws.Close()

	log.Println("New Client Connected")

	// Channel to signal stop
	done := make(chan struct{})

	// Goroutine: Read from WS (Frontend -> Bridge -> API)
	go func() {
		defer close(done)
		for {
			var msg IncomingMessage
			err := ws.ReadJSON(&msg)
			if err != nil {
				return
			}
			
			if msg.Action == "kill" {
				log.Printf("Kill request for Pod ID: %s", msg.PodID)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_, err := grpcClient.DeletePod(ctx, &pb.PodId{Id: msg.PodID})
				cancel()
				if err != nil {
					log.Printf("Failed to delete pod: %v", err)
				}
			}
		}
	}()

	// Subscribe to gRPC Stream (API -> Bridge -> Frontend)
	stream, err := grpcClient.Subscribe(context.Background(), &pb.Empty{})
	if err != nil {
		log.Printf("Error subscribing to gRPC: %v", err)
		return
	}

	for {
		select {
		case <-done:
			return
		default:
			evt, err := stream.Recv()
			if err != nil {
				log.Printf("Stream closed: %v", err)
				return
			}
			
			// Convert Proto to JSON
			msg := BridgeMessage{
				Type:     evt.Type,
				ID:       evt.Id,
				Name:     evt.Name,
				Status:   evt.Status,
				HP:       int(evt.Hp),
				Position: evt.Position,
			}
			
			if err := ws.WriteJSON(msg); err != nil {
				log.Println("write:", err)
				return
			}
		}
	}
}
