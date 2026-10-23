export namespace app {
	
	export class BlockDTO {
	    type: string;
	    text?: string;
	    thinking?: string;
	    toolUseId?: string;
	    toolName?: string;
	    input?: any;
	    isError?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BlockDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.text = source["text"];
	        this.thinking = source["thinking"];
	        this.toolUseId = source["toolUseId"];
	        this.toolName = source["toolName"];
	        this.input = source["input"];
	        this.isError = source["isError"];
	    }
	}
	export class CreateSessionInput {
	    sessionId?: string;
	    title?: string;
	    workDir?: string;
	
	    static createFrom(source: any = {}) {
	        return new CreateSessionInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessionId = source["sessionId"];
	        this.title = source["title"];
	        this.workDir = source["workDir"];
	    }
	}
	export class MessageDTO {
	    id: string;
	    role: string;
	    blocks: BlockDTO[];
	    stopReason?: string;
	    timeMs: number;
	
	    static createFrom(source: any = {}) {
	        return new MessageDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.role = source["role"];
	        this.blocks = this.convertValues(source["blocks"], BlockDTO);
	        this.stopReason = source["stopReason"];
	        this.timeMs = source["timeMs"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ProjectDTO {
	    path: string;
	    name: string;
	    addedAtMs: number;
	
	    static createFrom(source: any = {}) {
	        return new ProjectDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.addedAtMs = source["addedAtMs"];
	    }
	}
	export class ProviderConfigDTO {
	    provider: string;
	    baseUrl: string;
	    model: string;
	    hasApiKey: boolean;
	    apiKeyMasked?: string;
	    contextWindow: number;
	    maxOutputTokens: number;
	    permissionMode: string;
	    workDir: string;
	    initError?: string;
	
	    static createFrom(source: any = {}) {
	        return new ProviderConfigDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.baseUrl = source["baseUrl"];
	        this.model = source["model"];
	        this.hasApiKey = source["hasApiKey"];
	        this.apiKeyMasked = source["apiKeyMasked"];
	        this.contextWindow = source["contextWindow"];
	        this.maxOutputTokens = source["maxOutputTokens"];
	        this.permissionMode = source["permissionMode"];
	        this.workDir = source["workDir"];
	        this.initError = source["initError"];
	    }
	}
	export class ProviderConfigInput {
	    provider: string;
	    baseUrl?: string;
	    model?: string;
	    apiKey?: string;
	    contextWindow?: number;
	    maxOutputTokens?: number;
	
	    static createFrom(source: any = {}) {
	        return new ProviderConfigInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.baseUrl = source["baseUrl"];
	        this.model = source["model"];
	        this.apiKey = source["apiKey"];
	        this.contextWindow = source["contextWindow"];
	        this.maxOutputTokens = source["maxOutputTokens"];
	    }
	}
	export class SendMessageResult {
	    sessionId: string;
	    turnId: string;
	    running: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SendMessageResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessionId = source["sessionId"];
	        this.turnId = source["turnId"];
	        this.running = source["running"];
	    }
	}
	export class SessionInfoDTO {
	    id: string;
	    title: string;
	    workDir: string;
	    messages: number;
	    updatedAtMs: number;
	    running: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfoDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.workDir = source["workDir"];
	        this.messages = source["messages"];
	        this.updatedAtMs = source["updatedAtMs"];
	        this.running = source["running"];
	    }
	}
	export class UsageDTO {
	    inputTokens: number;
	    outputTokens: number;
	    cacheReadInputTokens: number;
	    cacheCreationInputTokens: number;
	
	    static createFrom(source: any = {}) {
	        return new UsageDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.cacheReadInputTokens = source["cacheReadInputTokens"];
	        this.cacheCreationInputTokens = source["cacheCreationInputTokens"];
	    }
	}
	export class SessionViewDTO {
	    id: string;
	    title: string;
	    workDir: string;
	    model: string;
	    permissionMode: string;
	    createdAtMs: number;
	    messages: MessageDTO[];
	    usage: UsageDTO;
	    contextTokens: number;
	    running: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionViewDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.workDir = source["workDir"];
	        this.model = source["model"];
	        this.permissionMode = source["permissionMode"];
	        this.createdAtMs = source["createdAtMs"];
	        this.messages = this.convertValues(source["messages"], MessageDTO);
	        this.usage = this.convertValues(source["usage"], UsageDTO);
	        this.contextTokens = source["contextTokens"];
	        this.running = source["running"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

