package network

import (
	"fmt"
	"net"
	"path/filepath"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/rs/zerolog/log"
	"github.com/threefoldtech/zosv2/modules/network/bridge"
	"github.com/threefoldtech/zosv2/modules/network/wireguard"
	"github.com/vishvananda/netlink"

	"github.com/threefoldtech/zosv2/modules/network/namespace"

	"github.com/threefoldtech/zosv2/modules"
	zosip "github.com/threefoldtech/zosv2/modules/network/ip"
)

type networker struct {
	nodeID      modules.NodeID
	storageDir  string
	netResAlloc NetResourceAllocator
}

// NewNetworker create a new modules.Networker that can be used with zbus
func NewNetworker(storageDir string, allocator NetResourceAllocator) modules.Networker {
	return &networker{
		storageDir:  storageDir,
		netResAlloc: allocator,
	}
}

var _ modules.Networker = (*networker)(nil)

// GetNetwork implements modules.Networker interface
func (n *networker) GetNetwork(id string) (*modules.Network, error) {
	// TODO check signature
	return n.netResAlloc.Get(id)
}

// ApplyNetResource implements modules.Networker interface
func (n *networker) ApplyNetResource(network *modules.Network) error {

	var resource *modules.NetResource
	for _, res := range network.Resources {
		if res.NodeID == n.nodeID {
			resource = &res
			break
		}
	}
	if resource == nil {
		return fmt.Errorf("not network resource for this node: %s", n.nodeID.ID)
	}

	return applyNetResource(n.storageDir, network.NetID, resource, network.AllocationNR)
}

func (n *networker) DeleteNetResource(network *modules.Network) error {
	var resource *modules.NetResource
	for _, res := range network.Resources {
		if res.NodeID == n.nodeID {
			resource = &res
			break
		}
	}
	if resource == nil {
		return fmt.Errorf("not network resource for this node: %s", n.nodeID.ID)
	}
	return deleteNetResource(resource, network.AllocationNR)
}

func applyNetResource(storageDir string, netID modules.NetID, netRes *modules.NetResource, allocNr int8) error {
	if err := createNetworkResource(netID, netRes, allocNr); err != nil {
		return err
	}

	if _, err := configureWG(storageDir, netRes, allocNr); err != nil {
		return err
	}
	return nil
}

// createNetworkResource creates a network namespace and a bridge
// and a wireguard interface and then move it interface inside
// the net namespace
func createNetworkResource(netID modules.NetID, resource *modules.NetResource, allorNr int8) error {
	var (
		nibble     = zosip.NewNibble(resource.Prefix, allorNr)
		netnsName  = nibble.NetworkName()
		bridgeName = nibble.BridgeName()
		wgName     = nibble.WiregardName()
		vethName   = nibble.VethName()
	)

	log.Info().Str("bridge", bridgeName).Msg("Create bridge")
	br, err := bridge.New(bridgeName)
	if err != nil {
		return err
	}

	log.Info().Str("namesapce", netnsName).Msg("Create namesapce")
	netns, err := namespace.Create(netnsName)
	if err != nil {
		return err
	}

	hostIface := &current.Interface{}
	var handler = func(hostNS ns.NetNS) error {
		log.Info().
			Str("namespace", netnsName).
			Str("veth", vethName).
			Msg("Create veth pair in net namespace")
		hostVeth, containerVeth, err := ip.SetupVeth(vethName, 1500, hostNS)
		if err != nil {
			return err
		}
		hostIface.Name = hostVeth.Name

		link, err := netlink.LinkByName(containerVeth.Name)
		if err != nil {
			return err
		}

		ipnetv6 := &resource.Prefix
		a, b, err := nibble.ToV4()
		if err != nil {
			return err
		}
		ip, ipnetv4, err := net.ParseCIDR(fmt.Sprintf("10.%d.%d.1/24", a, b))
		if err != nil {
			return err
		}
		ipnetv4.IP = ip

		for _, ipnet := range []*net.IPNet{ipnetv6, ipnetv4} {
			log.Info().Str("addr", ipnet.String()).Msg("set address on veth interface")
			addr := &netlink.Addr{IPNet: ipnet, Label: ""}
			if err = netlink.AddrAdd(link, addr); err != nil {
				return err
			}
		}

		return nil
	}
	if err := netns.Do(handler); err != nil {
		return err
	}

	hostVeth, err := netlink.LinkByName(hostIface.Name)
	if err != nil {
		return err
	}

	log.Info().
		Str("veth", vethName).
		Str("bridge", bridgeName).
		Msg("attach veth to bridge")
	if err := bridge.AttachNic(hostVeth, br); err != nil {
		return err
	}

	log.Info().Str("wg", wgName).Msg("create wireguard interface")
	wg, err := wireguard.New(wgName)
	if err != nil {
		return err
	}

	log.Info().
		Str("wg", wgName).
		Str("namespace", netnsName).
		Msg("move wireguard into network namespace")
	if err := namespace.SetLink(wg, netnsName); err != nil {
		return err
	}

	return nil
}

func deleteNetResource(resource *modules.NetResource, allocNr int8) error {
	var (
		nibble     = zosip.NewNibble(resource.Prefix, allocNr)
		netnsName  = nibble.NetworkName()
		bridgeName = nibble.BridgeName()
	)
	if err := bridge.Delete(bridgeName); err != nil {
		return err
	}
	return namespace.Delete(netnsName)
}

func configureWG(storageDir string, resource *modules.NetResource, allocNr int8) (wgtypes.Key, error) {
	var (
		nibble      = zosip.NewNibble(resource.Prefix, allocNr)
		netnsName   = nibble.NetworkName()
		wgName      = nibble.WiregardName()
		storagePath = filepath.Join(storageDir, nibble.Hex())
		key         wgtypes.Key
		err         error
	)

	key, err = wireguard.LoadKey(storagePath)
	if err != nil {
		key, err = wireguard.GenerateKey(storagePath)
		if err != nil {
			return key, err
		}
	}

	// configure wg iface
	peers := make([]wireguard.Peer, len(resource.Connected))
	for i, peer := range resource.Connected {
		if peer.Type != modules.ConnTypeWireguard {
			continue
		}

		a, b, err := nibble.ToV4()
		if err != nil {
			return key, err
		}
		peers[i] = wireguard.Peer{
			PublicKey: peer.Connection.Key,
			Endpoint:  endpoint(peer),
			AllowedIPs: []string{
				fmt.Sprintf("fe80::%s/128", nibble.Hex()),
				fmt.Sprintf("172.16.%d.%d/32", a, b),
			},
		}
	}

	netns, err := namespace.GetByName(netnsName)
	if err != nil {
		return key, err
	}

	var handler = func(_ ns.NetNS) error {

		wg, err := wireguard.GetByName(wgName)
		if err != nil {
			return err
		}

		log.Info().Msg("configure wireguard interface")
		if err = wg.Configure(resource.LinkLocal.String(), key.String(), peers); err != nil {
			return err
		}
		return nil
	}
	if err := netns.Do(handler); err != nil {
		return key, err
	}

	return key, nil
}

func endpoint(peer modules.Connected) string {
	var endpoint string
	if peer.Connection.IP.To16() != nil {
		endpoint = fmt.Sprintf("[%s]:%d", peer.Connection.IP.String(), peer.Connection.Port)
	} else {
		endpoint = fmt.Sprintf("%s:%d", peer.Connection.IP.String(), peer.Connection.Port)
	}
	return endpoint
}

func wgIP(prefix net.IPNet) (*net.IPNet, error) {
	prefixIP := []byte(prefix.IP.To16())
	id := prefixIP[6:8]
	_, ipnet, err := net.ParseCIDR(fmt.Sprintf("fe80::%x/64", id))
	if err != nil {
		return nil, err
	}
	return ipnet, nil
}
