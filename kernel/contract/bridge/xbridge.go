package bridge

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/xuperchain/xupercore/kernel/contract"
)

// LogConfig is the log config of node
type LogConfig struct {
	Module         string `yaml:"module,omitempty"`
	Filepath       string `yaml:"filepath,omitempty"`
	Filename       string `yaml:"filename,omitempty"`
	Fmt            string `yaml:"fmt,omitempty"`
	Console        bool   `yaml:"console,omitempty"`
	Level          string `yaml:"level,omitempty"`
	Async          bool   `yaml:"async,omitempty"`
	RotateInterval int    `yaml:"rotateinterval,omitempty"`
	RotateBackups  int    `yaml:"rotatebackups,omitempty"`
}

// ContractConfig define the config of XuperBridge
type ContractConfig struct {
	EnableDebugLog bool
	DebugLog       LogConfig
	EnableUpgrade  bool
}

// XBridge 用于注册用户虚拟机以及向Xchain Core注册可被识别的vm.VirtualMachine
type XBridge struct {
	ctxmgr         *ContextManager
	syscallService *SyscallService
	basedir        string
	vmconfigs      map[ContractType]VMConfig
	creators       map[ContractType]InstanceCreator
	xmodel         contract.XMReader
	config         ContractConfig

	// debugLogger *log.Logger

	*contractManager
}

type XBridgeConfig struct {
	Basedir   string
	VMConfigs map[ContractType]VMConfig
	XModel    contract.XMReader
	Config    ContractConfig
	LogWriter io.Writer
	Core      contract.ChainCore
}

// New instances a new XBridge
func New(cfg *XBridgeConfig) (*XBridge, error) {
	ctxmgr := NewContextManager()
	xbridge := &XBridge{
		ctxmgr:    ctxmgr,
		basedir:   cfg.Basedir,
		vmconfigs: cfg.VMConfigs,
		creators:  make(map[ContractType]InstanceCreator),
		xmodel:    cfg.XModel,
		config:    cfg.Config,
	}
	xbridge.contractManager = &contractManager{
		xbridge:      xbridge,
		codeProvider: newCodeProvider(cfg.XModel),
	}

	syscallService := NewSyscallService(ctxmgr, xbridge)
	xbridge.syscallService = syscallService
	err := xbridge.initVM()
	if err != nil {
		return nil, err
	}
	// err = xbridge.initDebugLogger(cfg)
	// if err != nil {
	// 	return nil, err
	// }
	return xbridge, nil
}

func (v *XBridge) initVM() error {
	types := []ContractType{TypeWasm, TypeNative, TypeEvm}
	for _, tp := range types {
		vmconfig, ok := v.vmconfigs[tp]
		if !ok {
			// log.Error("config for contract type not found", "type", tp)
			continue
		}
		if !vmconfig.IsEnable() {
			// log.Info("contract type disabled", "type", tp)
			continue
		}
		creatorConfig := &InstanceCreatorConfig{
			Basedir:        filepath.Join(v.basedir, vmconfig.DriverName()),
			SyscallService: v.syscallService,
			VMConfig:       vmconfig,
		}
		creator, err := Open(tp, vmconfig.DriverName(), creatorConfig)
		if err != nil {
			return err
		}
		v.creators[tp] = creator
	}
	return nil
}

// func (v *XBridge) initDebugLogger(cfg *XBridgeConfig) error {
// 	// 如果日志开启，并且没有自定义writter则使用配置文件打开日志对象
// 	if cfg.Config.EnableDebugLog && cfg.LogWriter == nil {
// 		debugLogger, err := log.OpenLog(&cfg.Config.DebugLog)
// 		if err != nil {
// 			return err
// 		}
// 		v.debugLogger = &debugLogger
// 		return nil
// 	}

// 	w := cfg.LogWriter
// 	if w == nil {
// 		w = ioutil.Discard
// 	}
// 	logger := log15.Root().New()
// 	logger.SetHandler(log15.StreamHandler(w, log15.LogfmtFormat()))
// 	v.debugLogger = &log.Logger{Logger: logger}
// 	return nil
// }

func (v *XBridge) getCreator(tp ContractType) InstanceCreator {
	return v.creators[tp]
}

func (v *XBridge) NewContext(ctxCfg *contract.ContextConfig) (contract.Context, error) {
	// test if contract exists
	desc, err := newCodeProvider(ctxCfg.State).GetContractCodeDesc(ctxCfg.ContractName)
	if err != nil {
		return nil, err
	}
	tp, err := getContractType(desc)
	if err != nil {
		return nil, err
	}
	vm := v.xbridge.getCreator(tp)
	if vm == nil {
		return nil, fmt.Errorf("vm for contract type %s not supported", tp)
	}
	var cp ContractCodeProvider
	// 如果当前在部署合约，合约代码从cache获取
	// 合约调用的情况则从model中拿取合约代码，避免交易中包含合约代码的引用。
	if ctxCfg.ContractCodeFromCache {
		cp = newCodeProvider(ctxCfg.State)
	} else {
		cp = newDescProvider(v.codeProvider, desc)
	}

	ctx := v.ctxmgr.MakeContext()
	ctx.State = ctxCfg.State
	ctx.ContractName = ctxCfg.ContractName
	ctx.Initiator = ctxCfg.Initiator
	ctx.AuthRequire = ctxCfg.AuthRequire
	ctx.ResourceLimits = ctxCfg.ResourceLimits
	ctx.CanInitialize = ctxCfg.CanInitialize
	// ctx.Core = ctxCfg.Core
	ctx.TransferAmount = ctxCfg.TransferAmount
	ctx.ContractSet = ctxCfg.ContractSet
	if ctx.ContractSet == nil {
		ctx.ContractSet = make(map[string]bool)
		ctx.ContractSet[ctx.ContractName] = true
	}
	// ctx.Logger = v.xbridge.debugLogger.New("contract", ctx.ContractName, "ctxid", ctx.ID)
	release := func() {
		v.ctxmgr.DestroyContext(ctx)
	}

	instance, err := vm.CreateInstance(ctx, cp)
	if err != nil {
		v.ctxmgr.DestroyContext(ctx)
		return nil, err
	}
	ctx.Instance = instance
	return &vmContextImpl{
		ctx:      ctx,
		instance: instance,
		release:  release,
	}, nil
}