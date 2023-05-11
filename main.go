package main

import (
	"github.com/pulumi/pulumi-gcp/sdk/v6/go/gcp/compute"
	"github.com/pulumi/pulumi-gcp/sdk/v6/go/gcp/dns"
	"github.com/pulumi/pulumi-gcp/sdk/v6/go/gcp/storage"
	synced "github.com/pulumi/pulumi-synced-folder/sdk/go/synced-folder"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"strings"
)

func getValueWithFallback(cfg *config.Config, key, fallback string) string {
	value := cfg.Get(key)
	if len(value) == 0 {
		// TODO Warn that we're using a default
		// best way to get ctx in?  seems dumb to pass in to every call
		// ctx.Log.Warn("warning", nil)
		return fallback
	}
	return value
}

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {

		// Import the program's configuration settings.
		cfg := config.New(ctx, "")
		path := getValueWithFallback(cfg, "path", "./build")
		indexDocument := getValueWithFallback(cfg, "indexDocument", "index.html")
		errorDocument := getValueWithFallback(cfg, "errorDocument", "error.html")
		frontendAppBucketName := getValueWithFallback(cfg, "frontendAppBucketName", "frontend-app-bucket")
		// TODO Add in your own default here
		domainName := getValueWithFallback(cfg, "domainName", "super-awesome-test-site.com")
		zoneName := strings.ReplaceAll(domainName, ".", "-")
		appHostName := getValueWithFallback(cfg, "appHostName", "app")

		//// Create a URLMap to route requests to the storage bucket.
		//_, err := firebase.NewWebApp(ctx, "gruff1", &firebase.WebAppArgs{
		//	//DeletionPolicy: pulumi.String("ABANDON"),
		//	DisplayName: pulumi.String("gruff1"),
		//})
		//if err != nil {
		//	return err
		//}

		// Create a storage bucket and configure it as a website.
		bucket, err := storage.NewBucket(ctx, frontendAppBucketName, &storage.BucketArgs{
			Location: pulumi.String("US"),
			Website: &storage.BucketWebsiteArgs{
				MainPageSuffix: pulumi.String(indexDocument),
				NotFoundPage:   pulumi.String(errorDocument),
			},
		})
		if err != nil {
			return err
		}

		// Create an IAM binding to allow public read access to the bucket.
		_, err = storage.NewBucketIAMBinding(ctx, "bucket-iam-binding", &storage.BucketIAMBindingArgs{
			Bucket: bucket.Name,
			Role:   pulumi.String("roles/storage.objectViewer"),
			Members: pulumi.StringArray{
				pulumi.String("allUsers"),
			},
		})
		if err != nil {
			return err
		}

		// Use a synced folder to manage the files of the website.
		_, err = synced.NewGoogleCloudFolder(ctx, "synced-folder", &synced.GoogleCloudFolderArgs{
			Path:       pulumi.String(path),
			BucketName: bucket.Name,
		})
		if err != nil {
			return err
		}

		// Enable the storage bucket as a CDN.
		backendBucket, err := compute.NewBackendBucket(ctx, "backend-bucket", &compute.BackendBucketArgs{
			BucketName: bucket.Name,
			EnableCdn:  pulumi.Bool(true),
		})
		if err != nil {
			return err
		}

		// Provision a global IP address for the CDN.
		ip, err := compute.NewGlobalAddress(ctx, "ip", nil)
		if err != nil {
			return err
		}

		envDnsZone, err := dns.LookupManagedZone(ctx, &dns.LookupManagedZoneArgs{
			Name: zoneName,
		}, nil)
		if err != nil {
			return err
		}

		_, err = dns.NewRecordSet(ctx, "dns", &dns.RecordSetArgs{
			Name:        pulumi.String(appHostName + "." + envDnsZone.DnsName),
			Type:        pulumi.String("A"),
			Ttl:         pulumi.Int(300),
			ManagedZone: pulumi.String(envDnsZone.Name),
			Rrdatas: pulumi.StringArray{
				ip.Address,
			},
		})
		if err != nil {
			return err
		}

		// Create a URLMap to route requests to the storage bucket.
		urlMap, err := compute.NewURLMap(ctx, "url-map", &compute.URLMapArgs{
			DefaultService: backendBucket.SelfLink,
		})
		if err != nil {
			return err
		}

		// Create an HTTP proxy to route requests to the URLMap.
		httpProxy, err := compute.NewTargetHttpProxy(ctx, "http-proxy", &compute.TargetHttpProxyArgs{
			UrlMap: urlMap.SelfLink,
		})
		if err != nil {
			return err
		}

		// Create a GlobalForwardingRule rule to route requests to the HTTP proxy.
		_, err = compute.NewGlobalForwardingRule(ctx, "http-forwarding-rule", &compute.GlobalForwardingRuleArgs{
			IpAddress:  ip.Address,
			IpProtocol: pulumi.String("TCP"),
			PortRange:  pulumi.String("80"),
			Target:     httpProxy.SelfLink,
		})
		if err != nil {
			return err
		}

		sslCertificate, err := compute.NewManagedSslCertificate(ctx, zoneName, &compute.ManagedSslCertificateArgs{
			Managed: compute.ManagedSslCertificateManagedArgs{
				Domains: pulumi.StringArray{
					pulumi.String(domainName),
					pulumi.String(appHostName + "." + domainName),
				},
			},
		})
		if err != nil {
			return err
		}

		// Create an HTTPS proxy to route requests to the URLMap.
		httpsProxy, err := compute.NewTargetHttpsProxy(ctx, "https-proxy", &compute.TargetHttpsProxyArgs{
			SslCertificates: pulumi.StringArray{sslCertificate.SelfLink},
			UrlMap:          urlMap.SelfLink,
		})
		if err != nil {
			return err
		}

		// Create a GlobalForwardingRule rule to route requests to the HTTPS proxy.
		_, err = compute.NewGlobalForwardingRule(ctx, "https-forwarding-rule", &compute.GlobalForwardingRuleArgs{
			IpAddress:  ip.Address,
			IpProtocol: pulumi.String("TCP"),
			PortRange:  pulumi.String("443"),
			Target:     httpsProxy.SelfLink,
		})

		if err != nil {
			return err
		}
		// Export the URLs and hostnames of the bucket and CDN.
		// These are worthless, not sure what I'd value here
		ctx.Export("originURL", pulumi.Sprintf("https://storage.googleapis.com/%v/index.html", bucket.Name))
		ctx.Export("originHostname", pulumi.Sprintf("storage.googleapis.com/%v", bucket.Name))
		ctx.Export("cdnURL", pulumi.Sprintf("http://%v", ip.Address))
		ctx.Export("cdnHostname", ip.Address)
		return nil
	})
}
