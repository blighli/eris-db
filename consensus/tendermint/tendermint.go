// Copyright 2015, 2016 Eris Industries (UK) Ltd.
// This file is part of Eris-RT

// Eris-RT is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// Eris-RT is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with Eris-RT.  If not, see <http://www.gnu.org/licenses/>.

// version provides the current Eris-DB version and a VersionIdentifier
// for the modules to identify their version with.

package tendermint

import (
	"fmt"
	"path"
	"strings"
	"sync"

	crypto "github.com/tendermint/go-crypto"
	p2p "github.com/tendermint/go-p2p"
	node "github.com/tendermint/tendermint/node"
	proxy "github.com/tendermint/tendermint/proxy"
	tendermint_types "github.com/tendermint/tendermint/types"
	tmsp_types "github.com/tendermint/tmsp/types"

	log "github.com/eris-ltd/eris-logger"

	config "github.com/eris-ltd/eris-db/config"
	definitions "github.com/eris-ltd/eris-db/definitions"
	manager_types "github.com/eris-ltd/eris-db/manager/types"
	rpc_tendermint_types "github.com/eris-ltd/eris-db/rpc/tendermint/core/types"
	// files  "github.com/eris-ltd/eris-db/files"
)

type TendermintNode struct {
	tmintNode   *node.Node
	tmintConfig *TendermintConfig
}

// NOTE [ben] Compiler check to ensure TendermintNode successfully implements
// eris-db/definitions.Consensus
var _ definitions.ConsensusEngine = (*TendermintNode)(nil)

func NewTendermintNode(moduleConfig *config.ModuleConfig,
	application manager_types.Application) (*TendermintNode, error) {
	// re-assert proper configuration for module
	if moduleConfig.Version != GetTendermintVersion().GetMinorVersionString() {
		return nil, fmt.Errorf("Version string %s did not match %s",
			moduleConfig.Version, GetTendermintVersion().GetMinorVersionString())
	}
	// loading the module has ensured the working and data directory
	// for tendermint have been created, but the config files needs
	// to be written in tendermint's root directory.
	// NOTE: [ben] as elsewhere Sub panics if config file does not have this
	// subtree. To shield in go-routine, or PR to viper.
	if !moduleConfig.Config.IsSet("configuration") {
		return nil, fmt.Errorf("Failed to extract Tendermint configuration subtree.")
	}
	tendermintConfigViper := moduleConfig.Config.Sub("configuration")
	if tendermintConfigViper == nil {
		return nil,
			fmt.Errorf("Failed to extract Tendermint configuration subtree.")
	}
	// wrap a copy of the viper config in a tendermint/go-config interface
	tmintConfig := GetTendermintConfig(tendermintConfigViper)
	// complete the tendermint configuration with default flags
	tmintConfig.AssertTendermintDefaults(moduleConfig.ChainId,
		moduleConfig.WorkDir, moduleConfig.DataDir, moduleConfig.RootDir)

	privateValidatorFilePath := path.Join(moduleConfig.RootDir,
		moduleConfig.Config.GetString("private_validator_file"))
	if moduleConfig.Config.GetString("private_validator_file") == "" {
		return nil, fmt.Errorf("No private validator file provided.")
	}
	// override tendermint configurations to force consistency with overruling
	// settings
	tmintConfig.AssertTendermintConsistency(moduleConfig,
		privateValidatorFilePath)
	log.WithFields(log.Fields{
		"chainId":              tmintConfig.GetString("chain_id"),
		"genesisFile":          tmintConfig.GetString("genesis_file"),
		"nodeLocalAddress":     tmintConfig.GetString("node_laddr"),
		"moniker":              tmintConfig.GetString("moniker"),
		"seeds":                tmintConfig.GetString("seeds"),
		"fastSync":             tmintConfig.GetBool("fast_sync"),
		"rpcLocalAddress":      tmintConfig.GetString("rpc_laddr"),
		"databaseDirectory":    tmintConfig.GetString("db_dir"),
		"privateValidatorFile": tmintConfig.GetString("priv_validator_file"),
		"privValFile":          moduleConfig.Config.GetString("private_validator_file"),
	}).Debug("Loaded Tendermint sub-configuration")
	// TODO: [ben] do not "or Generate Validator keys", rather fail directly
	// TODO: [ben] implement the signer for Private validator over eris-keys
	// TODO: [ben] copy from rootDir to tendermint workingDir;
	privateValidator := tendermint_types.LoadOrGenPrivValidator(
		path.Join(moduleConfig.RootDir,
			moduleConfig.Config.GetString("private_validator_file")))

	newNode := node.NewNode(tmintConfig, privateValidator, func(_, _ string,
		hash []byte) proxy.AppConn {
		return NewLocalClient(new(sync.Mutex), application)
	})

	listener := p2p.NewDefaultListener("tcp", tmintConfig.GetString("node_laddr"),
		tmintConfig.GetBool("skip_upnp"))

	newNode.AddListener(listener)
	// TODO: [ben] delay starting the node to a different function, to hand
	// control over events to Core
	if err := newNode.Start(); err != nil {
		newNode.Stop()
		return nil, fmt.Errorf("Failed to start Tendermint consensus node: %v", err)
	}
	log.WithFields(log.Fields{
		"nodeAddress":       tmintConfig.GetString("node_laddr"),
		"transportProtocol": "tcp",
		"upnp":              !tmintConfig.GetBool("skip_upnp"),
		"moniker":           tmintConfig.GetString("moniker"),
	}).Info("Tendermint consensus node started")

	// If seedNode is provided by config, dial out.
	if tmintConfig.GetString("seeds") != "" {
		seeds := strings.Split(tmintConfig.GetString("seeds"), ",")
		newNode.DialSeeds(seeds)
		log.WithFields(log.Fields{
			"seeds": seeds,
		}).Debug("Tendermint node called seeds")
	}

	return &TendermintNode{
		tmintNode:   newNode,
		tmintConfig: tmintConfig,
	}, nil
}

//------------------------------------------------------------------------------
// ConsensusEngine implementation

func (this *TendermintNode) Height() int {
	return this.tmintNode.BlockStore().Height()
}

func (this *TendermintNode) LoadBlockMeta(height int) *tendermint_types.BlockMeta {
	return this.tmintNode.BlockStore().LoadBlockMeta(height)
}

func (this *TendermintNode) IsListening() bool {
	return this.tmintNode.Switch().IsListening()
}

func (this *TendermintNode) Listeners() []p2p.Listener {
	var copyListeners []p2p.Listener
	// copy slice of Listeners
	copy(copyListeners[:], this.tmintNode.Switch().Listeners())
	return copyListeners
}

func (this *TendermintNode) Peers() []rpc_tendermint_types.Peer {
	peers := []rpc_tendermint_types.Peer{}
	for _, peer := range this.tmintNode.Switch().Peers().List() {
		peers = append(peers, rpc_tendermint_types.Peer{
			NodeInfo:   *peer.NodeInfo,
			IsOutbound: peer.IsOutbound(),
		})
	}
	return peers
}

func (this *TendermintNode) NodeInfo() *p2p.NodeInfo {
	var copyNodeInfo = new(p2p.NodeInfo)
	// call Switch().NodeInfo is not go-routine safe, so copy
	*copyNodeInfo = *this.tmintNode.Switch().NodeInfo()
	return copyNodeInfo
}

func (this *TendermintNode) PublicValidatorKey() crypto.PubKey {
	// TODO: [ben] this is abetment, not yet a go-routine safe solution
	var copyPublicValidatorKey crypto.PubKey
	// crypto.PubKey is an interface so copy underlying struct
	publicKey := this.tmintNode.PrivValidator().PubKey
	switch publicKey.(type) {
	case crypto.PubKeyEd25519:
		// type crypto.PubKeyEd25519 is [32]byte
		copyKeyBytes := publicKey.(crypto.PubKeyEd25519)
		copyPublicValidatorKey = crypto.PubKey(copyKeyBytes)
	default:
		// TODO: [ben] add error return to all these calls
		copyPublicValidatorKey = nil
	}
	return copyPublicValidatorKey
}

func (this *TendermintNode) BroadcastTransaction(transaction []byte,
	callback func(*tmsp_types.Response)) error {
	return this.tmintNode.MempoolReactor().BroadcastTx(transaction, callback)
}

//------------------------------------------------------------------------------
// Helper functions

// func marshalConfigToDisk(filePath string, tendermintConfig *viper.Viper) error {
//
//   tendermintConfig.Unmarshal
//   // marshal interface to toml bytes
//   bytesConfig, err := toml.Marshal(tendermintConfig)
//   if err != nil {
//     return fmt.Fatalf("Failed to marshal Tendermint configuration to bytes: %v",
//       err)
//   }
//   return files.WriteAndBackup(filePath, bytesConfig)
// }
