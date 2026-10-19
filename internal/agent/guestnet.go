package agent

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

func resolvBody(servers []string) string {
	var b strings.Builder
	for _, s := range servers {
		if s == "" {
			continue
		}
		fmt.Fprintf(&b, "nameserver %s\n", s)
	}
	return b.String()
}

func nlAlign(n int) int { return (n + 3) &^ 3 }

func putAttr(dst []byte, typ uint16, data []byte) []byte {
	length := 4 + len(data)
	dst = append(dst, byte(length), byte(length>>8), byte(typ), byte(typ>>8))
	dst = append(dst, data...)
	if pad := nlAlign(length) - length; pad > 0 {
		dst = append(dst, make([]byte, pad)...)
	}
	return dst
}

func nlHeader(typ, flags uint16, bodyLen int) []byte {
	buf := make([]byte, 16+bodyLen)
	binary.LittleEndian.PutUint32(buf[0:], uint32(16+bodyLen))
	binary.LittleEndian.PutUint16(buf[4:], typ)
	binary.LittleEndian.PutUint16(buf[6:], flags)
	return buf[:16]
}

// addrMessage builds an RTM_NEWADDR request for ip on ifindex.
func addrMessage(ifindex int, ip net.IP, prefix int) ([]byte, error) {
	v4 := ip.To4()
	if v4 == nil {
		return nil, fmt.Errorf("guest address %s is not ipv4", ip)
	}
	body := make([]byte, 8)
	body[0] = 2 // AF_INET
	body[1] = byte(prefix)
	body[3] = 0 // RT_SCOPE_UNIVERSE
	binary.LittleEndian.PutUint32(body[4:], uint32(ifindex))
	body = putAttr(body, 1, v4) // IFA_ADDRESS
	body = putAttr(body, 2, v4) // IFA_LOCAL
	// RTM_NEWADDR with NLM_F_REQUEST|NLM_F_ACK|NLM_F_CREATE|NLM_F_REPLACE.
	hdr := nlHeader(20, 0x1|0x4|0x400|0x100, len(body))
	return append(hdr, body...), nil
}

// routeMessage builds a default-route RTM_NEWROUTE via gw on ifindex.
func routeMessage(ifindex int, gw net.IP) ([]byte, error) {
	v4 := gw.To4()
	if v4 == nil {
		return nil, fmt.Errorf("gateway %s is not ipv4", gw)
	}
	body := make([]byte, 12)
	body[0] = 2                 // family
	body[1] = 0                 // dst len
	body[4] = 254               // RT_TABLE_MAIN
	body[5] = 3                 // RTPROT_BOOT
	body[6] = 0                 // universe
	body[7] = 1                 // unicast
	body = putAttr(body, 5, v4) // RTA_GATEWAY
	oif := make([]byte, 4)
	binary.LittleEndian.PutUint32(oif, uint32(ifindex))
	body = putAttr(body, 4, oif)                        // RTA_OIF
	hdr := nlHeader(24, 0x1|0x4|0x400|0x100, len(body)) // RTM_NEWROUTE
	return append(hdr, body...), nil
}
