package handlers

import (
	"example.com/Quaver/Z/multiplayer"
	"example.com/Quaver/Z/packets"
	"example.com/Quaver/Z/sessions"
)

// Handles when the client sends judgements in the multiplayer match
func handleClientGameJudgements(user *sessions.User, packet *packets.ClientGameJudgements) {
	if packet == nil {
		return
	}

	game := multiplayer.GetGameById(user.GetMultiplayerGameId())

	if game == nil {
		return
	}

	game.RunLocked(func() {
		game.HandlePlayerJudgements(user.Info.Id, packet.Judgements)
	})
}
