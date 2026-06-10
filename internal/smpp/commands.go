package smpp

const (
	CommandBindReceiver        = commandBindReceiver
	CommandBindTransmitter     = commandBindTransmitter
	CommandBindTransceiver     = commandBindTransceiver
	CommandSubmitSM            = commandSubmitSM
	CommandDeliverSM           = commandDeliverSM
	CommandEnquireLink         = commandEnquireLink
	CommandUnbind              = commandUnbind
	CommandGenericNack         = commandGenericNack
	CommandBindReceiverResp    = commandBindReceiverResp
	CommandBindTransmitterResp = commandBindTransmitterResp
	CommandBindTransceiverResp = commandBindTransceiverResp
	CommandSubmitSMResp        = commandSubmitSMResp
	CommandDeliverSMResp       = commandDeliverSMResp
	CommandEnquireLinkResp     = commandEnquireLinkResp
	CommandUnbindResp          = commandUnbindResp
	StatusOK                   = statusOK
	StatusInvalidCmd           = statusInvalidCmd
	StatusInvalidBind          = statusInvalidBind
	StatusBindFailed           = statusBindFailed
	StatusThrottled            = statusThrottled
	StatusInvalidPassword      = statusInvalidPassword
	StatusInvalidSystemID      = statusInvalidSystemID
	StatusInvalidSrcAddr       = 0x0000000A
	StatusInvalidDestAddr      = 0x0000000B
	StatusMsgQFull             = 0x00000014
	StatusSubmitFailed         = 0x00000045
)
