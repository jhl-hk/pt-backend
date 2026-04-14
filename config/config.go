package config

import (
	"gopkg.in/yaml.v3"
	"os"
)

type Neighbor struct {
	Address  string `yaml:"address"`
	ASN      uint32 `yaml:"asn"`
	Multihop bool   `yaml:"multihop"`
}

type Global struct {
	ASN      uint32 `yaml:"asn"`
	RouterID string `yaml:"router_id"`
	Port     int    `yaml:"port"`
}

type Database struct {
	DSN string `yaml:"dsn"`
}

type Config struct {
	Global    Global     `yaml:"global"`
	Neighbors []Neighbor `yaml:"neighbors"`
	Database  Database   `yaml:"database"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}
