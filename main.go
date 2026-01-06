package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"

	// Kubernetes Client
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	k8sClient  *kubernetes.Clientset
	
	// Connections pool to broadcast events to all connected WS clients
	wsClients   = make(map[*websocket.Conn]bool)
	broadcast   = make(chan BridgeMessage)
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
	// 1. Initialize Kubernetes Client (Informer)
	initKubernetesClient()

	// 2. Start Broadcaster
	go handleMessages()

	// 3. Probes
	http.HandleFunc("/liveness", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
		if k8sClient != nil {
			w.Write([]byte("ok"))
		} else {
			w.WriteHeader(500)
		}
	})
	http.HandleFunc("/startup", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// 4. Start WebSocket Server
	http.HandleFunc("/ws", handleWebSocket)
	
	log.Println("Bridge Server listening on :9090")
	if err := http.ListenAndServe(":9090", nil); err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}

func initKubernetesClient() {
	var config *rest.Config
	var err error

	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		config, err = rest.InClusterConfig()
	} else {
		var kubeconfig *string
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
		}
		flag.Parse()
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}

	if err != nil {
		log.Printf("Error creating K8s config: %v", err)
		return
	}

	k8sClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("Error creating K8s client: %v", err)
		return
	}
	
	log.Println("Connected to Kubernetes Cluster (Bridge)")

	// Start Informer
	factory := informers.NewSharedInformerFactory(k8sClient, time.Minute*10)
	podInformer := factory.Core().V1().Pods().Informer()

	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			handleK8sEvent("ADDED", pod)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			pod := newObj.(*corev1.Pod)
			handleK8sEvent("MODIFIED", pod)
		},
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			handleK8sEvent("DELETED", pod)
		},
	})
	
	stopCh := make(chan struct{})
	factory.Start(stopCh)
}

func handleK8sEvent(eventType string, k8sPod *corev1.Pod) {
	if k8sPod.Namespace != "default" {
		return 
	}

	// Simple Sim: Map Name hash to Position
	hash := 0
	for _, c := range k8sPod.Name {
		hash += int(c)
	}
	x := float64(hash%10 - 5)
	z := float64((hash/10)%10 - 5)

	msg := BridgeMessage{
		Type:     eventType,
		ID:       string(k8sPod.UID),
		Name:     k8sPod.Name,
		Status:   string(k8sPod.Status.Phase),
		HP:       5,
		Position: []float64{x, 0, z},
	}
	
	broadcast <- msg
}

func handleMessages() {
	for {
		msg := <-broadcast
		for client := range wsClients {
			err := client.WriteJSON(msg)
			if err != nil {
				log.Printf("Websocket error: %v", err)
				client.Close()
				delete(wsClients, client)
			}
		}
	}
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}

	wsClients[ws] = true
	log.Println("New Client Connected")

	for {
		var msg IncomingMessage
		err := ws.ReadJSON(&msg)
		if err != nil {
			delete(wsClients, ws)
			break
		}
		
		if msg.Action == "kill" && msg.PodID != "" {
			log.Printf("Kill request for UID: %s", msg.PodID)
			go deletePod(msg.PodID)
		} else if msg.Action == "create" {
			log.Println("Create request")
			go createPod()
		}
	}
}

func deletePod(uid string) {
	pods, err := k8sClient.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Println("Failed to list pods:", err)
		return
	}
	
	var podName string
	for _, p := range pods.Items {
		if string(p.UID) == uid {
			podName = p.Name
			break
		}
	}
	
	if podName != "" {
		k8sClient.CoreV1().Pods("default").Delete(context.TODO(), podName, metav1.DeleteOptions{})
		log.Printf("Deleted Pod: %s", podName)
	} else {
		log.Printf("Pod with UID %s not found", uid)
	}
}

func createPod() {
	podName := "nginx-" + time.Now().Format("150405")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{ "app": "demo" },
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{ Name: "nginx", Image: "nginx:alpine" },
			},
		},
	}
	_, err := k8sClient.CoreV1().Pods("default").Create(context.TODO(), pod, metav1.CreateOptions{})
	if err != nil {
		log.Println("Failed to create pod:", err)
	}
}
