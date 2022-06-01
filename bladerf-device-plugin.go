package main

import (
    "flag"
    "fmt"
    "github.com/fsnotify/fsnotify"
    "github.com/golang/glog"
    "golang.org/x/net/context"
    "google.golang.org/grpc"
    "net"
    "path"
    "strconv"
    "strings"
    "sync"
    "time"
    "os"
    "os/exec"

    pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

type bladerfManager struct {
    devices map[string]*pluginapi.Device
    deviceFiles map[string]string
}

func NewBladeRFManager() (*bladerfManager, error) {
    return &bladerfManager{
        devices:        make(map[string]*pluginapi.Device),
        deviceFiles:    make(map[string]string),
    }, nil
}

func (mgr *bladerfManager) discoverBladeRFs() bool {
    found := false
    mgr.devices = make(map[string]*pluginapi.Device)
    mgr.deviceFiles = make(map[string]string)
    glog.Info("Discover bladerfs")

    out, err := exec.Command("bladeRF-cli", "-p").Output()

    if err != nil {
        glog.Fatal(err)
    }
    outs := string(out)
    glog.Info(outs)
    id := ""
    bus := -1
    flds := strings.Fields(outs)

    for idx, fld := range(flds) {
        if strings.Contains(fld, "Serial") {
            id = flds[idx+1]
        } else if strings.Contains(fld, "Bus") {
            bus, err = strconv.Atoi(flds[idx+1])
            if err != nil {
                glog.Warning(err)
            } else {
                glog.Info(bus)
            }
        } else if strings.Contains(fld, "Address") {
            addr, err := strconv.Atoi(flds[idx+1])
            if err != nil {
                glog.Warning(err)
                continue
            } else {
                glog.Info(addr)
            }
            if len(id) > 0 && bus >= 0 {
                pth := fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, addr)
                dev := pluginapi.Device{ID: id, Health: pluginapi.Healthy}
                mgr.devices[id] = &dev
                mgr.deviceFiles[id] = pth
                glog.Info("Found device ", id, " in ", pth)
                found = true
                id = ""
                bus = -1
            }
        }
    }

    return found
}

func (mgr *bladerfManager) GetDevicePluginOptions(_ context.Context, _ *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
    glog.Infoln("GetDevicePluginOptions")
    opts := new (pluginapi.DevicePluginOptions)
    opts.PreStartRequired = false
    opts.GetPreferredAllocationAvailable = false
    return opts, nil
}

func (mgr *bladerfManager) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
    glog.Info("device-plugin: ListAndWatch start")
    for {  // loop forever
        mgr.discoverBladeRFs()
        resp := new(pluginapi.ListAndWatchResponse)
        for _, dev := range mgr.devices {
            resp.Devices = append(resp.Devices, dev)
        }
        glog.Info("Sending ", len(resp.Devices), " devices")
        if err := stream.Send(resp); err != nil {
            glog.Errorf("Failed to send response to kubelet: %v\n", err)
        }
        time.Sleep(5 * time.Second)  // this could be replaced with a usb/filesystem watcher
    }
}

func (mgr *bladerfManager) GetPreferredAllocation(_ context.Context, _ *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
    // not used
    return nil, nil
}

func (mgr *bladerfManager) Allocate(_ context.Context, request *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
    glog.Info("Allocate")
    resp := new(pluginapi.AllocateResponse)

    for _, ar := range request.ContainerRequests {
        cr := new(pluginapi.ContainerAllocateResponse)
        resp.ContainerResponses = append(resp.ContainerResponses, cr)
        for _, id := range ar.DevicesIDs {
            if _, ok := mgr.deviceFiles[id]; ok {
                cr.Devices = append(cr.Devices, &pluginapi.DeviceSpec{
                    ContainerPath: mgr.deviceFiles[id],
                    HostPath: mgr.deviceFiles[id],
                    Permissions: "rw",
                })
            }
        }
    }

    return resp, nil
}

func (mgr *bladerfManager) PreStartContainer(_ context.Context, _ *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
    // not used
    return nil, nil
}

func Register(kubeletEndpoint string, pluginEndpoint string, resourceName string) error {
    glog.Infoln("Register")
    conn, err := grpc.Dial(kubeletEndpoint, grpc.WithInsecure(),
        grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
            return net.DialTimeout("unix", addr, timeout)
        }))
    if err != nil {
        glog.Fatalf("device-plugin: cannot connect to kubelet service: %v", err)
        return err
    }
    defer func(conn *grpc.ClientConn) {
        _ = conn.Close()
    }(conn)

    client := pluginapi.NewRegistrationClient(conn)
    rr := &pluginapi.RegisterRequest{
        Version:        pluginapi.Version,
        Endpoint:       pluginEndpoint,
        ResourceName:   resourceName,
    }

    _, err = client.Register(context.Background(), rr)
    if err != nil {
        glog.Fatalf("device-plugin: cannot register to kubelet service: %v", err)
    }
    return nil
}

func main() {
    flag.Parse()
    glog.Infoln("Starting main.")

    var socketName = "bladerf"
    var resourceName = "nuand.com/bladerf"

    glog.Info("Socket: ", socketName)
    glog.Info("Resource: ", resourceName)

    srv, err := NewBladeRFManager()
    pluginEndpoint := fmt.Sprintf("%s-%d.sock", socketName, time.Now().Unix())

    var wg sync.WaitGroup
    wg.Add(1)
    go func() {
        defer wg.Done()
        endpoint := path.Join(pluginapi.DevicePluginPath, pluginEndpoint)
        glog.Info("Endpoint: ", endpoint)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
            glog.Fatal(err)
            return
	}
        defer watcher.Close()
        go func() {
            for {
                select {
                case event, ok := <-watcher.Events:
                    if !ok {
                        return
                    }
                    if event.Op&fsnotify.Remove == fsnotify.Remove {
                        // kubelet restarted, need to re-register
                        glog.Fatal("Can't handle kubelet restart, we'll exit")
                        os.Exit(-1)
                    }
                case _, ok := <-watcher.Errors:
                    if !ok {
                        return
                    }
                }
            }
        }()
        lis, err := net.Listen("unix", endpoint)
        if err != nil {
            glog.Fatal(err)
            return
        }

        err = watcher.Add(endpoint)
        if err != nil {
            glog.Error(err)
        }

        grpcServer := grpc.NewServer()
        glog.Infoln("Register device plugin server")
        pluginapi.RegisterDevicePluginServer(grpcServer, srv)
        err = grpcServer.Serve(lis)
        if err != nil {
            glog.Fatal(err)
            return
        }
    }()

    time.Sleep(5 * time.Second)
    err = Register(pluginapi.KubeletSocket, pluginEndpoint, resourceName)
    if err != nil {
        glog.Fatal(err)
    }
    glog.Infoln("device-plugin registration complete")
    wg.Wait()
}
