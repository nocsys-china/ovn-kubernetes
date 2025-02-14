package addressset

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"

	"k8s.io/apimachinery/pkg/util/sets"
	utilnet "k8s.io/utils/net"

	"github.com/onsi/gomega"
)

func NewFakeAddressSetFactory() *FakeAddressSetFactory {
	return &FakeAddressSetFactory{
		sets: make(map[string]*fakeAddressSet),
	}
}

type FakeAddressSetFactory struct {
	sync.Mutex
	// maps address set name to object
	sets map[string]*fakeAddressSet
}

// fakeFactory implements the AddressSetFactory interface
var _ AddressSetFactory = &FakeAddressSetFactory{}

// NewAddressSet returns a new address set object
func (f *FakeAddressSetFactory) NewAddressSet(name string, ips []net.IP) (AddressSet, error) {
	f.Lock()
	defer f.Unlock()
	_, ok := f.sets[name]
	gomega.Expect(ok).To(gomega.BeFalse())
	set, err := newFakeAddressSets(name, ips, f.removeAddressSet)
	if err != nil {
		return nil, err
	}
	ip4ASName, ip6ASName := MakeAddressSetName(name)
	if set.ipv4 != nil {
		f.sets[ip4ASName] = set.ipv4
	}
	if set.ipv6 != nil {
		f.sets[ip6ASName] = set.ipv6
	}
	return set, nil
}

// EnsureAddressSet returns set object
func (f *FakeAddressSetFactory) EnsureAddressSet(name string) (AddressSet, error) {
	f.Lock()
	defer f.Unlock()
	_, ok := f.sets[name]
	gomega.Expect(ok).To(gomega.BeFalse())
	set, err := newFakeAddressSets(name, []net.IP{}, f.removeAddressSet)
	if err != nil {
		return nil, err
	}
	ip4ASName, ip6ASName := MakeAddressSetName(name)
	if set.ipv4 != nil {
		f.sets[ip4ASName] = set.ipv4
	}
	if set.ipv6 != nil {
		f.sets[ip6ASName] = set.ipv6
	}
	return set, nil
}

func (f *FakeAddressSetFactory) ProcessEachAddressSet(iteratorFn AddressSetIterFunc) error {
	f.Lock()
	defer f.Unlock()
	asNames := sets.String{}
	for _, set := range f.sets {
		asName := truncateSuffixFromAddressSet(set.getName())
		if asNames.Has(asName) {
			continue
		}
		asNames.Insert(asName)
		parts := strings.Split(asName, ".")
		addrSetNamespace := parts[0]
		nameSuffix := ""
		if len(parts) >= 2 {
			nameSuffix = parts[1]
		}
		if err := iteratorFn(asName, addrSetNamespace, nameSuffix); err != nil {
			return err
		}
	}
	return nil
}

func (f *FakeAddressSetFactory) DestroyAddressSetInBackingStore(name string) error {
	if _, ok := f.sets[name]; ok {
		f.removeAddressSet(name)
		return nil
	}
	ip4ASName, ip6ASName := MakeAddressSetName(name)
	if config.IPv4Mode {
		f.removeAddressSet(ip4ASName)
	}
	if config.IPv6Mode {
		f.removeAddressSet(ip6ASName)
	}
	return nil
}

func (f *FakeAddressSetFactory) getAddressSet(name string) *fakeAddressSet {
	f.Lock()
	defer f.Unlock()
	if as, ok := f.sets[name]; ok {
		as.Lock()
		return as
	}
	return nil
}

// removeAddressSet removes the address set from the factory
func (f *FakeAddressSetFactory) removeAddressSet(name string) {
	f.Lock()
	defer f.Unlock()
	delete(f.sets, name)
}

// ExpectAddressSetWithIPs ensures the named address set exists with the given set of IPs
func (f *FakeAddressSetFactory) ExpectAddressSetWithIPs(name string, ips []string) {
	var lenAddressSet int
	name4, name6 := MakeAddressSetName(name)
	as4 := f.getAddressSet(name4)
	if as4 != nil {
		defer as4.Unlock()
		lenAddressSet = lenAddressSet + len(as4.ips)
	}
	as6 := f.getAddressSet(name6)
	if as6 != nil {
		defer as6.Unlock()
		lenAddressSet = lenAddressSet + len(as6.ips)
	}

	for _, ip := range ips {
		if utilnet.IsIPv6(net.ParseIP(ip)) {
			gomega.Expect(as6).NotTo(gomega.BeNil())
			gomega.Expect(as6.ips).To(gomega.HaveKey(ip))
		} else {
			gomega.Expect(as4).NotTo(gomega.BeNil())
			gomega.Expect(as4.ips).To(gomega.HaveKey(ip))
		}
	}

	gomega.Expect(lenAddressSet).To(gomega.Equal(len(ips)))
}

func (f *FakeAddressSetFactory) EventuallyExpectAddressSetWithIPs(name string, ips []string) {
	gomega.Eventually(func() {
		f.ExpectAddressSetWithIPs(name, ips)
	}).Should(gomega.Succeed())
}

// ExpectEmptyAddressSet ensures the named address set exists with no IPs
func (f *FakeAddressSetFactory) ExpectEmptyAddressSet(name string) {
	f.ExpectAddressSetWithIPs(name, nil)
}

// EventuallyExpectEmptyAddressSetExist ensures the named address set eventually exists with no IPs
func (f *FakeAddressSetFactory) EventuallyExpectEmptyAddressSetExist(name string) {
	f.EventuallyExpectAddressSetWithIPs(name, nil)
}

// ExpectAddressSetExist ensures the named address set eventually exiss
func (f *FakeAddressSetFactory) ExpectAddressSetExist(name string) {
	gomega.Eventually(func() bool {
		f.Lock()
		defer f.Unlock()
		_, ok := f.sets[name]
		return ok
	}).Should(gomega.BeTrue())
}

// EventuallyExpectNoAddressSet ensures the named address set eventually does not exist
func (f *FakeAddressSetFactory) EventuallyExpectNoAddressSet(name string) {
	gomega.Eventually(func() {
		f.ExpectAddressSetExist(name)
	}).ShouldNot(gomega.Succeed())
}

type removeFunc func(string)

type fakeAddressSet struct {
	sync.Mutex
	name      string
	hashName  string
	ips       map[string]net.IP
	destroyed uint32
	removeFn  removeFunc
}

// fakeAddressSets implements the AddressSet interface
var _ AddressSet = &fakeAddressSets{}

type fakeAddressSets struct {
	sync.Mutex
	name string
	ipv4 *fakeAddressSet
	ipv6 *fakeAddressSet
}

func newFakeAddressSets(name string, ips []net.IP, removeFn removeFunc) (*fakeAddressSets, error) {
	var v4set, v6set *fakeAddressSet
	v4Ips := make([]net.IP, 0)
	v6Ips := make([]net.IP, 0)
	for _, ip := range ips {
		if utilnet.IsIPv6(ip) {
			v6Ips = append(v6Ips, ip)
		} else {
			v4Ips = append(v4Ips, ip)
		}
	}
	ip4ASName, ip6ASName := MakeAddressSetName(name)
	if config.IPv4Mode {
		v4set = newFakeAddressSet(ip4ASName, v4Ips, removeFn)
	}
	if config.IPv6Mode {
		v6set = newFakeAddressSet(ip6ASName, v6Ips, removeFn)
	}
	return &fakeAddressSets{name: name, ipv4: v4set, ipv6: v6set}, nil
}

func newFakeAddressSet(name string, ips []net.IP, removeFn removeFunc) *fakeAddressSet {
	as := &fakeAddressSet{
		name:     name,
		hashName: hashedAddressSet(name),
		ips:      make(map[string]net.IP),
		removeFn: removeFn,
	}
	for _, ip := range ips {
		as.ips[ip.String()] = ip
	}
	return as
}

func (as *fakeAddressSets) GetASHashNames() (string, string) {
	var ipv4AS string
	var ipv6AS string
	if as.ipv4 != nil {
		ipv4AS = as.ipv4.getHashName()
	}
	if as.ipv6 != nil {
		ipv6AS = as.ipv6.getHashName()
	}
	return ipv4AS, ipv6AS
}

func (as *fakeAddressSets) GetName() string {
	return as.name
}

func (as *fakeAddressSets) AddIPs(ips []net.IP) error {
	var err error
	as.Lock()
	defer as.Unlock()

	_, err = as.AddIPsReturnOps(ips)
	return err
}

func (as *fakeAddressSets) AddIPsReturnOps(ips []net.IP) ([]ovsdb.Operation, error) {
	var ops []ovsdb.Operation
	var err error

	for _, ip := range ips {
		if utilnet.IsIPv6(ip) {
			ops, err = as.ipv6.addIP(ip)
		} else {
			ops, err = as.ipv4.addIP(ip)
		}
		if err != nil {
			return nil, err
		}
	}
	return ops, nil
}

func (as *fakeAddressSets) GetIPs() ([]string, []string) {
	as.Lock()
	defer as.Unlock()

	var v4ips []string
	var v6ips []string

	if as.ipv6 != nil {
		v6ips, _ = as.ipv6.getIPs()
	}
	if as.ipv4 != nil {
		v4ips, _ = as.ipv4.getIPs()
	}

	return v4ips, v6ips
}

func (as *fakeAddressSets) SetIPs(ips []net.IP) error {
	// NOOP
	return nil
}

func (as *fakeAddressSets) DeleteIPs(ips []net.IP) error {
	var err error
	as.Lock()
	defer as.Unlock()

	_, err = as.DeleteIPsReturnOps(ips)
	return err
}

func (as *fakeAddressSets) DeleteIPsReturnOps(ips []net.IP) ([]ovsdb.Operation, error) {
	var ops []ovsdb.Operation
	var err error

	for _, ip := range ips {
		if utilnet.IsIPv6(ip) {
			ops, err = as.ipv6.deleteIP(ip)
		} else {
			ops, err = as.ipv4.deleteIP(ip)
		}
		if err != nil {
			return nil, err
		}
	}
	return ops, nil
}

func (as *fakeAddressSets) Destroy() error {
	as.Lock()
	defer as.Unlock()

	if as.ipv4 != nil {
		err := as.ipv4.destroy()
		if err != nil {
			return err
		}
	}
	if as.ipv6 != nil {
		return as.ipv6.destroy()
	}
	return nil
}

func (as *fakeAddressSet) getHashName() string {
	gomega.Expect(atomic.LoadUint32(&as.destroyed)).To(gomega.Equal(uint32(0)))
	return as.hashName
}

func (as *fakeAddressSet) getName() string {
	gomega.Expect(atomic.LoadUint32(&as.destroyed)).To(gomega.Equal(uint32(0)))
	return as.name
}

func (as *fakeAddressSet) addIP(ip net.IP) ([]ovsdb.Operation, error) {
	as.Lock()
	defer as.Unlock()
	gomega.Expect(atomic.LoadUint32(&as.destroyed)).To(gomega.Equal(uint32(0)))
	ipStr := ip.String()
	if _, ok := as.ips[ipStr]; !ok {
		as.ips[ip.String()] = ip
	}
	return nil, nil
}

func (as *fakeAddressSet) getIPs() ([]string, error) {
	as.Lock()
	defer as.Unlock()
	gomega.Expect(atomic.LoadUint32(&as.destroyed)).To(gomega.Equal(uint32(0)))
	uniqIPs := make([]string, 0, len(as.ips))
	for _, ip := range as.ips {
		uniqIPs = append(uniqIPs, ip.String())
	}
	return uniqIPs, nil
}

func (as *fakeAddressSet) deleteIP(ip net.IP) ([]ovsdb.Operation, error) {
	as.Lock()
	defer as.Unlock()
	gomega.Expect(atomic.LoadUint32(&as.destroyed)).To(gomega.Equal(uint32(0)))
	delete(as.ips, ip.String())
	return nil, nil
}

func (as *fakeAddressSet) destroy() error {
	gomega.Expect(atomic.LoadUint32(&as.destroyed)).To(gomega.Equal(uint32(0)))
	atomic.StoreUint32(&as.destroyed, 1)
	as.removeFn(as.name)
	return nil
}
