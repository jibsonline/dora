package connectors

import (
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"net"
	"sync"

	"github.com/jinzhu/gorm"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"gitlab.booking.com/infra/dora/model"
	"gitlab.booking.com/infra/dora/storage"
)

const (
	// Blade is the constant defining the blade hw type
	Blade = "blade"
	// Discrete is the constant defining the Discrete hw type
	Discrete = "discrete"
	// Chassis is the constant defining the chassis hw type
	Chassis = "chassis"
	// HP is the constant that defines the vendor HP
	HP = "HP"
	// Dell is the constant that defines the vendor Dell
	Dell = "Dell"
	// Supermicro is the constant that defines the vendor Supermicro
	Supermicro = "Supermicro"
	// Common is the constant of thinks we could use across multiple vendors
	Common = "Common"
	// Unknown is the constant that defines Unknowns vendors
	Unknown = "Unknown"
)

// Connection is used to connect and later dicover the hardware information we have for each vendor
type Connection struct {
	username string
	password string
	host     string
	vendor   string
	hwtype   string
}

// Vendor returns the vendor of the current connection
func (c *Connection) Vendor() (vendor string) {
	return c.vendor
}

// HwType returns hwtype of the current connection
func (c *Connection) HwType() (hwtype string) {
	return c.hwtype
}

func (c *Connection) detect() (err error) {
	log.WithFields(log.Fields{"step": "connection", "host": c.host}).Info("Detecting vendor")

	client, err := buildClient()
	if err != nil {
		return err
	}

	resp, err := client.Get(fmt.Sprintf("https://%s/xmldata?item=all", c.host))
	if err != nil {
		return err
	}

	if resp.StatusCode == 200 {
		payload, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		iloXMLC := &HpRimp{}
		err = xml.Unmarshal(payload, iloXMLC)
		if err != nil {
			return err
		}

		if iloXMLC.HpInfra2 != nil {
			c.vendor = HP
			c.hwtype = Chassis
			return err
		}

		iloXML := &HpRimpBlade{}
		err = xml.Unmarshal(payload, iloXML)
		if err != nil {
			fmt.Println(err)
			return err
		}

		if iloXML.HpBladeBlade != nil {
			c.vendor = HP
			c.hwtype = Blade
			return err
		} else if iloXML.HpMP != nil && iloXML.HpBladeBlade == nil {
			c.vendor = HP
			c.hwtype = Discrete
			return err
		}

		return err
	}

	resp, err = client.Get(fmt.Sprintf("https://%s/data/login", c.host))
	if err != nil {
		return err
	}

	if resp.StatusCode == 200 {
		c.vendor = Dell
		c.hwtype = Blade
		return err
	}

	resp, err = client.Get(fmt.Sprintf("https://%s/cgi-bin/webcgi/login", c.host))
	if err != nil {
		return err
	}

	if resp.StatusCode == 200 {
		c.vendor = Dell
		c.hwtype = Chassis
		return err
	}

	resp, err = client.Get(fmt.Sprintf("https://%s/cgi/login.cgi", c.host))
	if err != nil {
		return err
	}

	if resp.StatusCode == 200 {
		c.vendor = Supermicro
		c.hwtype = Discrete
		return err
	}

	return ErrVendorUnknown
}

// NewConnection creates a new connection and detects the vendor and model of the given hardware
func NewConnection(username string, password string, host string) (c *Connection, err error) {
	c = &Connection{username: username, password: password, host: host}
	err = c.detect()
	return c, err
}

func (c *Connection) blade(bmc Bmc) (blade *model.Blade) {
	blade = &model.Blade{}
	var err error

	blade.BmcAddress = c.host
	blade.Vendor = c.Vendor()

	blade.BmcType, err = bmc.BmcType()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading bmc type", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	blade.BmcVersion, err = bmc.BmcVersion()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading bmc version", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	blade.Serial, err = bmc.Serial()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading serial", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	blade.Model, err = bmc.Model()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading model", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	blade.Nics, err = bmc.Nics()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading nics", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	err = bmc.Login()
	if err != nil {
		log.WithFields(log.Fields{"operation": "bmc auth", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	} else {
		defer bmc.Logout()
		blade.BmcAuth = true

		blade.BiosVersion, err = bmc.BiosVersion()
		if err != nil {
			log.WithFields(log.Fields{"operation": "reading bios version", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
		}

		blade.Processor, blade.ProcessorCount, blade.ProcessorCoreCount, blade.ProcessorThreadCount, err = bmc.CPU()
		if err != nil {
			log.WithFields(log.Fields{"operation": "reading cpu", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
		}

		blade.Memory, err = bmc.Memory()
		if err != nil {
			log.WithFields(log.Fields{"operation": "reading memory", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
		}

		blade.Status, err = bmc.Status()
		if err != nil {
			log.WithFields(log.Fields{"operation": "reading status", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
		}

		blade.Name, err = bmc.Name()
		if err != nil {
			log.WithFields(log.Fields{"operation": "reading name", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
		}

		blade.TempC, err = bmc.TempC()
		if err != nil {
			log.WithFields(log.Fields{"operation": "reading thermal data", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
		}

		blade.PowerKw, err = bmc.PowerKw()
		if err != nil {
			log.WithFields(log.Fields{"operation": "reading power usage data", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
		}

		blade.BmcLicenceType, blade.BmcLicenceStatus, err = bmc.License()
		if err != nil {
			log.WithFields(log.Fields{"operation": "reading license data", "ip": blade.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
		}
	}

	return blade
}

func (c *Connection) chassis(ch BmcChassis) (chassis *model.Chassis) {
	chassis = &model.Chassis{}
	var err error

	chassis.Vendor = c.Vendor()
	chassis.BmcAddress = c.host
	chassis.Name, err = ch.Name()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading name", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.Serial, err = ch.Serial()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading serial", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.Model, err = ch.Model()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading model", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.PowerKw, err = ch.PowerKw()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading power usage", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.TempC, err = ch.TempC()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading thermal data", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.Status, err = ch.Status()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading status", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.FwVersion, err = ch.FwVersion()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading firmware version", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.PowerSupplyCount, err = ch.PowerSupplyCount()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading psu count", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.PassThru, err = ch.PassThru()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading passthru", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	chassis.Blades, err = ch.Blades()
	if err != nil {
		log.WithFields(log.Fields{"operation": "reading blades", "ip": chassis.BmcAddress, "vendor": c.Vendor, "type": c.HwType, "error": err}).Warning("Auditing hardware")
	}

	db := storage.InitDB()

	scans := []model.ScannedPort{}
	db.Where("scanned_host_ip = ?", chassis.BmcAddress).Find(&scans)
	for _, scan := range scans {
		if scan.Port == 443 && scan.Protocol == "tcp" && scan.State == "open" {
			chassis.BmcWEBReachable = true
		} else if scan.Port == 22 && scan.Protocol == "tcp" && scan.State == "open" {
			chassis.BmcSSHReachable = true
		}
	}

	return chassis
}

// Collect collects all relevant data of the current hardwand and returns the populated object
func (c *Connection) Collect() (i interface{}, err error) {
	if c.vendor == HP && (c.hwtype == Blade || c.hwtype == Discrete) {
		ilo, err := NewIloReader(&c.host, &c.username, &c.password)
		if err != nil {
			return i, err
		}
		return c.blade(ilo), err
	} else if c.vendor == HP && c.hwtype == Chassis {
		c7000, err := NewHpChassisReader(&c.host, &c.username, &c.password)
		if err != nil {
			return i, err
		}
		return c.chassis(c7000), err
	} /* else if c.vendor == Dell && (c.hwtype == Blade || c.hwtype == Discrete) {
		redfish, err := NewRedFishReader(&c.host, &c.username, &c.password)
		if err != nil {
			return i, err
		}
		return c.blade(redfish), err
	} */

	return i, err
}

func collect(input <-chan string, db *gorm.DB) {
	bmcUser := viper.GetString("bmc_user")
	bmcPass := viper.GetString("bmc_pass")

	for host := range input {
		c, err := NewConnection(bmcUser, bmcPass, host)
		if err != nil {
			log.WithFields(log.Fields{"operation": "connection", "ip": host, "type": c.HwType(), "error": err}).Error(fmt.Sprintf("Connecting to host"))
		}
		if c.HwType() != Blade {
			data, err := c.Collect()
			if err != nil {
				log.WithFields(log.Fields{"operation": "connection", "ip": host, "type": c.HwType(), "error": err}).Error(fmt.Sprintf("Collecting data"))
			}
			switch data.(type) {
			case *model.Chassis:
				chassisStorage := storage.NewChassisStorage(db)
				_, err = chassisStorage.UpdateOrCreate(data.(*model.Chassis))
			case *model.Blade:
				bladeStorage := storage.NewBladeStorage(db)
				_, err = bladeStorage.UpdateOrCreate(data.(*model.Blade))
			}
		}
	}
}

// DataCollection collects the data of all given ips
func DataCollection(ips []string) {
	concurrency := viper.GetInt("concurrency")

	cc := make(chan string, concurrency)
	wg := sync.WaitGroup{}
	db := storage.InitDB()

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(input <-chan string, db *gorm.DB, wg *sync.WaitGroup) {
			defer wg.Done()
			collect(input, db)
		}(cc, db, &wg)
	}

	if ips[0] == "all" {
		hosts := []model.ScannedPort{}
		if err := db.Where("port = 443 and protocol = 'tcp' and state = 'open'").Find(&hosts).Error; err != nil {
			log.WithFields(log.Fields{"operation": "connection", "ip": "all", "error": err}).Error(fmt.Sprintf("Retrieving scanned hosts"))
		} else {
			for _, host := range hosts {
				cc <- host.ScannedHostIP
			}
		}
	} else {
		for _, ip := range ips {
			host := model.ScannedPort{}
			parsedIP := net.ParseIP(ip)
			if parsedIP == nil {
				lookup, err := net.LookupHost(ip)
				if err != nil {
					log.WithFields(log.Fields{"operation": "connection", "ip": ip, "error": err}).Error(fmt.Sprintf("Retrieving scanned hosts"))
					continue
				}
				ip = lookup[0]
			}

			if err := db.Where("scanned_host_ip = ? and port = 443 and protocol = 'tcp' and state = 'open'", ip).Find(&host).Error; err != nil {
				log.WithFields(log.Fields{"operation": "connection", "ip": ip, "error": err}).Error(fmt.Sprintf("Retrieving scanned hosts"))
				continue
			}
			cc <- host.ScannedHostIP
		}
	}

	close(cc)
	wg.Wait()
}