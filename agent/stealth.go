package agent

import (
	browse "go.klarlabs.de/scout"
	"go.klarlabs.de/scout/middleware"
)

// applyStealthPatches injects anti-detection scripts into the page.
// It registers the stealth JS via addScriptToEvaluateOnNewDocument for future
// navigations and also evaluates it immediately for the current page context.
// Must be called with the session mutex held.
func (s *Session) applyStealthPatches(page *browse.Page) {
	_, _ = page.Call("Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source": stealthAgentJS,
	})
	_, _ = page.Evaluate(stealthAgentJS)

	ua := middleware.RandomUserAgent()
	_ = page.SetUserAgent(ua)
}

// stealthAgentJS is the same stealth script used by middleware.Stealth(),
// duplicated here to avoid a circular dependency between agent and middleware.
// This must stay in sync with the stealthJS constant in middleware/stealth.go.
const stealthAgentJS = `
// 1. Override navigator.webdriver
Object.defineProperty(navigator, 'webdriver', {
	get: () => false,
	configurable: true
});

// 2. Mock chrome.runtime to pass chrome.runtime check
if (!window.chrome) window.chrome = {};
if (!window.chrome.runtime) {
	window.chrome.runtime = {
		connect: function() {},
		sendMessage: function() {},
		id: undefined
	};
}

// 3. Override Permissions API to deny 'notifications' query gracefully
if (navigator.permissions) {
	const originalQuery = navigator.permissions.query.bind(navigator.permissions);
	navigator.permissions.query = function(parameters) {
		if (parameters.name === 'notifications') {
			return Promise.resolve({ state: Notification.permission });
		}
		return originalQuery(parameters);
	};
}

// 4. Override navigator.plugins to appear non-empty
Object.defineProperty(navigator, 'plugins', {
	get: () => {
		const plugins = [
			{ name: 'Chrome PDF Plugin', filename: 'internal-pdf-viewer', description: 'Portable Document Format' },
			{ name: 'Chrome PDF Viewer', filename: 'mhjfbmdgcfjbbpaeojofohoefgiehjai', description: '' },
			{ name: 'Native Client', filename: 'internal-nacl-plugin', description: '' }
		];
		plugins.length = 3;
		return plugins;
	},
	configurable: true
});

// 5. Override navigator.languages
Object.defineProperty(navigator, 'languages', {
	get: () => ['en-US', 'en'],
	configurable: true
});

// 6. Mask WebGL vendor/renderer
const getParameter = WebGLRenderingContext.prototype.getParameter;
WebGLRenderingContext.prototype.getParameter = function(parameter) {
	if (parameter === 37445) return 'Intel Inc.';
	if (parameter === 37446) return 'Intel Iris OpenGL Engine';
	return getParameter.call(this, parameter);
};

// 7. Fix broken iframe contentWindow
const origAttachShadow = Element.prototype.attachShadow;
Element.prototype.attachShadow = function(init) {
	if (init && init.mode) return origAttachShadow.call(this, init);
	return origAttachShadow.call(this, { mode: 'open' });
};

// 8. Remove automation-related properties
delete navigator.__proto__.webdriver;

// 9. Fix window.outerWidth/outerHeight (headless has 0)
if (window.outerWidth === 0) {
	Object.defineProperty(window, 'outerWidth', { get: () => window.innerWidth });
	Object.defineProperty(window, 'outerHeight', { get: () => window.innerHeight + 85 });
}

// 10. Fix missing screen properties in headless
if (screen.availWidth === 0) {
	Object.defineProperty(screen, 'availWidth', { get: () => screen.width });
	Object.defineProperty(screen, 'availHeight', { get: () => screen.height - 40 });
}

// 11. Spoof navigator.connection
if ('connection' in navigator || 'mozConnection' in navigator || 'webkitConnection' in navigator) {
	const connProps = { effectiveType: '4g', rtt: 50, downlink: 10, saveData: false };
	const conn = navigator.connection || navigator.mozConnection || navigator.webkitConnection;
	if (conn) {
		for (const [k, v] of Object.entries(connProps)) {
			Object.defineProperty(conn, k, { get: () => v, configurable: true });
		}
	}
} else {
	Object.defineProperty(navigator, 'connection', {
		get: () => ({ effectiveType: '4g', rtt: 50, downlink: 10, saveData: false }),
		configurable: true
	});
}

// 12. Randomize navigator.hardwareConcurrency
Object.defineProperty(navigator, 'hardwareConcurrency', {
	get: () => [4, 8, 12, 16][(Math.random() * 4) | 0],
	configurable: true
});

// 13. Spoof navigator.deviceMemory
Object.defineProperty(navigator, 'deviceMemory', {
	get: () => [4, 8][(Math.random() * 2) | 0],
	configurable: true
});

// 14. Canvas fingerprint noise
const origGetImageData = CanvasRenderingContext2D.prototype.getImageData;
CanvasRenderingContext2D.prototype.getImageData = function() {
	const imageData = origGetImageData.apply(this, arguments);
	const data = imageData.data;
	for (let i = 0; i < data.length; i += 4) {
		data[i]     = data[i]     ^ (Math.random() < 0.1 ? 1 : 0);
		data[i + 1] = data[i + 1] ^ (Math.random() < 0.1 ? 1 : 0);
		data[i + 2] = data[i + 2] ^ (Math.random() < 0.1 ? 1 : 0);
	}
	return imageData;
};

// 15. AudioContext fingerprint noise
if (typeof AnalyserNode !== 'undefined') {
	const origGetFloat = AnalyserNode.prototype.getFloatFrequencyData;
	AnalyserNode.prototype.getFloatFrequencyData = function(array) {
		origGetFloat.call(this, array);
		for (let i = 0; i < array.length; i++) {
			array[i] += (Math.random() - 0.5) * 0.001;
		}
	};
}

// 16. WebRTC leak prevention
if (typeof RTCPeerConnection !== 'undefined') {
	const OrigRTC = RTCPeerConnection;
	window.RTCPeerConnection = function(config) {
		if (config && config.iceServers) {
			config.iceServers = [];
		}
		const pc = new OrigRTC(config);
		const origCreateOffer = pc.createOffer.bind(pc);
		pc.createOffer = function(opts) {
			return origCreateOffer(opts).then(function(offer) {
				offer.sdp = offer.sdp.replace(/a=candidate:.+typ host.+\r\n/g, '');
				return offer;
			});
		};
		return pc;
	};
	window.RTCPeerConnection.prototype = OrigRTC.prototype;
}

// 17. Override navigator.permissions.query for notifications
if (navigator.permissions) {
	const origPermQuery = navigator.permissions.query.bind(navigator.permissions);
	navigator.permissions.query = function(desc) {
		if (desc && desc.name === 'notifications') {
			return Promise.resolve({ state: 'prompt', onchange: null });
		}
		return origPermQuery(desc);
	};
}
`
