# Message selection and keyboard contract

## Scope and identity

Message lists in Inbox, Reading, Receipts, Snoozed, server folders and search use the same selection state. A row is identified by account plus message ID, never by its displayed index. Bulk actions apply only to explicitly selected rows still present in the currently loaded list; with no explicit selection, keyboard verbs act on the focused row.

Select-all means all **loaded** rows. It never silently selects unfetched pages or another account/search. New pages do not become selected automatically. Date separators are not rows.

## Pointer behavior

- Plain primary click preserves Lull Mail's existing open-to-read behavior, clears any previous explicit selection, and sets the focus/range anchor to that row
- Ctrl-click on Windows/Linux or Command-click on macOS toggles one row without opening it or marking it read; either modifier is accepted by the implementation
- Shift-click replaces selection with the inclusive range between the anchor and clicked row. The anchor stays fixed for repeated range changes, so extending, reversing and shrinking work
- Ctrl/Command+Shift-click adds the inclusive range to the existing selection
- Without an anchor, Shift uses the focused row; without either, it selects only the clicked row
- The selection button toggles one row and establishes focus/anchor. Shift on that button uses the same range rule and never also opens the row
- Nested links, quick-action buttons, inputs and editors retain their own behavior. Quick actions apply to their own row. Modified gestures on ordinary row text select; actual nested links keep native browser navigation
- Non-primary and Alt clicks are left alone

## Keyboard behavior

- j/k and Down/Up move focus without replacing explicit selection; a normal move establishes the next range anchor
- Shift+Down/Up extends or shrinks the anchored range in message lists. Ctrl/Command+Shift adds the range. Ctrl/Command+Down/Up moves focus without changing the anchor or selected rows
- Home/End focus the first/last loaded message; Shift selects the corresponding range
- x toggles the focused row. Space also toggles in message lists; Space/Enter on a selection button keep native button activation. Holding a selection/action key does not repeat that action
- Ctrl/Command+A selects all loaded messages while the list or bulk bar owns the key; editing fields and unrelated focused links/controls keep their native select-all behavior
- Enter/o opens the focused thread and clears explicit selection
- Existing e, s, i, p, r, c, u, g-navigation and search shortcuts remain. Help lists the new selection keys; browser Delete/Backspace are not reassigned to destructive mail operations
- Escape first dismisses the active menu/overlay, then any toast, then visible explicit selection, then the reader. Clearing selection retains row focus. A local snooze menu consumes its Escape and does not also clear the underlying selection
- Typing fields, contenteditable regions, IME composition, locally handled events and native button/link activation are respected. Global Ctrl/Command+K still opens the palette

Today and Board retain their existing x-selection keyboard scope and now show selected-item feedback. Today exposes the existing mail bulk actions. Board shows only a count and Clear because its card actions and mail actions have different meanings. Its response carries authoritative read/bucket/snooze state for current pins; missing or unsupported state disables mail keyboard mutations rather than inventing an undo snapshot. Such pins retain their Open/Done/Unpin card controls. Derived Inbox cards keep their known unread state. Board Done checks off the card, while keyboard e marks its mail read. The additional range/select-all/Space gestures are scoped to message lists.

## Focus, refresh and navigation

Focus and selected membership follow identities across insertions and reordering. Missing rows are removed from selection; a removed focused row resets focus, and a removed anchor is discarded. Ranges always use the current displayed order. Changing list key/kind, query or account scope clears selection/anchor rather than reusing numeric positions.

First-page and continuation requests are fenced by request ownership. Old account/query responses and old load-more responses cannot publish into a newer scope or refreshed first page. Repeated load-more activation admits only one request. Same-scope refresh keeps the previous rows while pending, then replaces them with refreshed page one; removed selected rows are pruned. Page failure retains a retry cursor and reports the failure. Stable row arrays prevent selection-only rerenders from starting a publish/render loop.

In document layout, or the narrow-screen fallback of classic layout, the visible reader owns keyboard mail verbs. Hidden-list movement, range selection and opening are disabled; reader scrolling and native text selection remain available. In wide classic layout, the visible list stays keyboard-actionable beside the reader.

Route changes, Back/Forward, search and account changes dismiss the old reader and fence its old list immediately. Returning with u/Back restores the saved list scroll; changing destinations does not restore an unrelated old offset. Superseded restoration callbacks are cancelled.

## Behavioral reference

Roundcube was consulted as a behavioral reference; no source code was copied. Its [message-list documentation](https://docs.roundcube.net/doc/help/1.1/en_US/mail/mailview.html#selecting-multiple-messages) describes modifier selection. At inspected revision `a8bbc7122d7bdce5cd57e19d8711d13a158abf06`, [list.js](https://github.com/roundcube/roundcubemail/blob/a8bbc7122d7bdce5cd57e19d8711d13a158abf06/program/js/list.js) distinguishes a fixed range anchor, individual toggles and additive ranges, while [common.js](https://github.com/roundcube/roundcubemail/blob/a8bbc7122d7bdce5cd57e19d8711d13a158abf06/program/js/common.js) maps Command on macOS and Control elsewhere. Lull Mail retains its existing open-on-click and j/k focus behavior rather than copying every Roundcube binding.

## Verification

The regression suites exercise real Preact components, event bubbling, active-element focus, bulk actions, summary selection feedback, routing/palette interactions and deferred hook responses. Run `npm test` and `npm run typecheck` in `dashboard`.

`npm run test:selection` starts a local synthetic-mail Vite fixture and a headless Chromium test; it does not connect to a real mailbox. `CHROMIUM_PATH` can select an existing browser executable and `E2E_OUTPUT` selects the screenshot directory. The authoring environment refused Chromium's temporary local socket, so this browser test has not yet completed. A normal browser run and live-provider/backend checks remain required before release.
