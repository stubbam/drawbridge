/** What does something of its own when it's clicked, so a click on it isn't a click on the card. */
const interactive = 'a, button, select, input, textarea, label, summary';

/**
 * Whether a click on a card opens the card's page. It doesn't when the click was on a link, a
 * button, or a control inside the card, which do their own thing, or when it ends a selection
 * of the card's text, so the text can still be copied by dragging over it.
 */
export function clickOpensCard(target: EventTarget | null, selected: string): boolean {
	if (selected) return false;
	const el = target as { closest?: (selector: string) => unknown } | null;
	return !el?.closest?.(interactive);
}
