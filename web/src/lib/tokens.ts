/**
 * The widget to paste into Homepage's services.yaml (gethomepage.dev, "Custom API"), for a token.
 * It reads the server's status. The address is the one the browser is on, which is where Homepage
 * has to reach it too, unless it's in a container that reaches the host another way.
 */
export function homepageWidget(origin: string, secret: string): string {
	return [
		'widget:',
		'  type: customapi',
		`  url: ${origin}/api/server/status`,
		'  headers:',
		`    Authorization: Bearer ${secret}`,
		'  mappings:',
		'    - field: online',
		'      label: Online',
		'    - field: clients',
		'      label: Clients',
		'    - field: paused',
		'      label: Paused',
		''
	].join('\n');
}
