package outside

// legoProviderNames is every name lego's provider registry switches on, sorted. huddle builds only
// the one-token providers in providerBuilders; linking lego's registry itself would pull every
// provider's SDK into the binary. The list tells a provider lego knows (refused as needing more
// than one credential) from a name nobody knows, and a test holds it to the registry.
var legoProviderNames = []string{
	"acme-dns", "acmedns", "active24", "alidns", "aliesa", "allinkl", "alwaysdata", "anexia",
	"artfiles", "arvancloud", "auroradns", "autodns", "axelname", "azion", "azure", "azuredns",
	"baiducloud", "beget", "binarylane", "bindman", "bluecat", "bluecatv2", "bookmyname", "brandit",
	"bunny", "checkdomain", "civo", "clouddns", "cloudflare", "cloudns", "cloudru", "cloudxns",
	"com35", "conoha", "conohav3", "constellix", "corenetworks", "cpanel", "czechia", "ddnss",
	"derak", "desec", "designate", "digitalocean", "directadmin", "dnsexit", "dnshomede", "dnsimple",
	"dnsmadeeasy", "dnspod", "dnsupdate", "dode", "domainnameshop", "domeneshop", "dreamhost",
	"duckdns", "dyn", "dyndnsfree", "dynu", "easydns", "edgecenter", "edgedns", "edgeone",
	"efficientip", "epik", "eurodns", "excedo", "exec", "exoscale", "f5xc", "fastdns", "freemyip",
	"gandi", "gandiv5", "gcloud", "gcore", "gigahostno", "glesys", "godaddy", "googledomains",
	"gravity", "hetzner", "hostingde", "hostinger", "hostingnl", "hosttech", "httpnet", "httpreq",
	"huaweicloud", "hurricane", "hyperone", "ibmcloud", "iij", "iijdpf", "infoblox", "infomaniak",
	"internetbs", "inwx", "ionos", "ionoscloud", "ipv64", "ispconfig", "ispconfigddns", "iwantmyname",
	"jdcloud", "joker", "keyhelp", "leaseweb", "liara", "lightsail", "limacity", "linode", "linodev4",
	"liquidweb", "loopia", "luadns", "mailinabox", "manageengine", "manual", "metaname",
	"metaregistrar", "mijnhost", "mittwald", "myaddr", "mydnsjp", "mythicbeasts", "namecheap",
	"namedotcom", "namesilo", "namesurfer", "nearlyfreespeech", "neodigit", "netcup", "netlify",
	"netnod", "nicmanager", "nicru", "nifcloud", "njalla", "nodion", "ns1", "octenium", "onecloudru",
	"onlinenet", "oraclecloud", "otc", "ovh", "pdns", "plesk", "porkbun", "rackspace", "rainyun",
	"rcodezero", "regfish", "regru", "rfc2136", "rimuhosting", "route53", "safedns", "sakuracloud",
	"scaleway", "selectel", "selectelv2", "selfhostde", "servercow", "shellrent", "simply", "sonic",
	"spaceship", "stackpath", "syse", "technitium", "tencentcloud", "timewebcloud", "todaynic",
	"transip", "ucloud", "ultradns", "uniteddomains", "variomedia", "vegadns", "vercel", "versio",
	"vinyldns", "virtualname", "vkcloud", "volcengine", "vscale", "vultr", "webnames", "webnamesca",
	"webnamesru", "websupport", "wedos", "westcn", "yandex", "yandex360", "yandexcloud", "zoneedit",
	"zoneee", "zonomi",
}
