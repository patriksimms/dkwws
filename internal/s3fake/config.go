package s3fake

import "github.com/patriksimms/dkwws/internal/config"

// Config returns settings pointing at this fake backend, so tests exercise the
// real configuration and signing path rather than a stub client.
func (s *Server) Config(accessKeyID, secretKey string) config.Config {
	return config.Config{
		Endpoint:        s.URL,
		Region:          config.DefaultRegion,
		Bucket:          s.bucket,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretKey,
		PathStyle:       true,
	}
}
