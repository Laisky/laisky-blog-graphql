package memory

import sdkmemory "github.com/Laisky/go-utils/v6/agents/memory"

// toSDKBeforeTurnInput converts request DTO to SDK input.
func toSDKBeforeTurnInput(request BeforeTurnRequest) sdkmemory.BeforeTurnInput {
	return sdkmemory.BeforeTurnInput{
		Project:           request.Project,
		SessionID:         request.SessionID,
		UserID:            request.UserID,
		TurnID:            request.TurnID,
		ConversationItems: request.ConversationItems,
		CurrentInputStart: request.CurrentInputStart,
		CurrentInputCount: request.CurrentInputCount,
		CurrentInput:      request.CurrentInput,
		BaseInstructions:  request.BaseInstructions,
		MaxInputTok:       request.MaxInputTok,
	}
}

// toSDKAfterTurnInput converts request DTO to SDK input.
func toSDKAfterTurnInput(request AfterTurnRequest) sdkmemory.AfterTurnInput {
	return sdkmemory.AfterTurnInput{
		Project:           request.Project,
		SessionID:         request.SessionID,
		UserID:            request.UserID,
		TurnID:            request.TurnID,
		ConversationItems: request.ConversationItems,
		CurrentInputStart: request.CurrentInputStart,
		CurrentInputCount: request.CurrentInputCount,
		InputItems:        request.InputItems,
		OutputItems:       request.OutputItems,
	}
}
