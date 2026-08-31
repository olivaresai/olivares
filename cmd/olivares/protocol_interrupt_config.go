// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func parseProtocolInterruptRoute(
	label, channelText, senderText, recipientText string,
) (sessions.ProtocolInterruptRoute, error) {
	parse := func(name, raw string) (model.ID, error) {
		raw = strings.TrimSpace(raw)
		id, err := model.ParseID(raw)
		if err != nil || id.IsZero() || id.String() != raw {
			return model.ID(""), fmt.Errorf("%s requires a canonical %s", label, name)
		}
		return id, nil
	}
	channelID, err := parse("interrupt_channel_id", channelText)
	if err != nil {
		return sessions.ProtocolInterruptRoute{}, err
	}
	senderID, err := parse("interrupt_sender_user_id", senderText)
	if err != nil {
		return sessions.ProtocolInterruptRoute{}, err
	}
	recipientID, err := parse("interrupt_recipient_user_id", recipientText)
	if err != nil {
		return sessions.ProtocolInterruptRoute{}, err
	}
	if senderID == recipientID {
		return sessions.ProtocolInterruptRoute{}, fmt.Errorf(
			"%s requires distinct interrupt sender and recipient users", label,
		)
	}
	return sessions.ProtocolInterruptRoute{
		ChannelID: channelID, SenderUserID: senderID, RecipientUserID: recipientID,
	}, nil
}
