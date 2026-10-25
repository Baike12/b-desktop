export namespace app {
	
	export class BackgroundTaskDTO {
	    id: string;
	    kind: string;
	    sessionId: string;
	    description: string;
	    command?: string;
	    agentType?: string;
	    status: string;
	    startedAt: string;
	    finishedAt?: string;
	    exitCode?: number;
	    error?: string;
	    result?: string;
	    outputFile?: string;
	
	    static createFrom(source: any = {}) {
	        return new BackgroundTaskDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.sessionId = source["sessionId"];
	        this.description = source["description"];
	        this.command = source["command"];
	        this.agentType = source["agentType"];
	        this.status = source["status"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.exitCode = source["exitCode"];
	        this.error = source["error"];
	        this.result = source["result"];
	        this.outputFile = source["outputFile"];
	    }
	}
	export class ImageDTO {
	    mediaType: string;
	    data: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mediaType = source["mediaType"];
	        this.data = source["data"];
	    }
	}
	export class BlockDTO {
	    type: string;
	    text?: string;
	    thinking?: string;
	    toolUseId?: string;
	    toolName?: string;
	    input?: any;
	    isError?: boolean;
	    mediaType?: string;
	    data?: string;
	    images?: ImageDTO[];
	
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
	        this.mediaType = source["mediaType"];
	        this.data = source["data"];
	        this.images = this.convertValues(source["images"], ImageDTO);
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
	export class FileChangeDTO {
	    path: string;
	    oldPath: string;
	    status: string;
	    staged: boolean;
	    additions: number;
	    deletions: number;
	    binary: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileChangeDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.oldPath = source["oldPath"];
	        this.status = source["status"];
	        this.staged = source["staged"];
	        this.additions = source["additions"];
	        this.deletions = source["deletions"];
	        this.binary = source["binary"];
	    }
	}
	export class ChangeSetDTO {
	    root: string;
	    branch: string;
	    isRepo: boolean;
	    changes: FileChangeDTO[];
	
	    static createFrom(source: any = {}) {
	        return new ChangeSetDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.root = source["root"];
	        this.branch = source["branch"];
	        this.isRepo = source["isRepo"];
	        this.changes = this.convertValues(source["changes"], FileChangeDTO);
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
	export class CompactResultDTO {
	    preTokens: number;
	    postTokens: number;
	    summary?: string;
	
	    static createFrom(source: any = {}) {
	        return new CompactResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.preTokens = source["preTokens"];
	        this.postTokens = source["postTokens"];
	        this.summary = source["summary"];
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
	export class ModelUsageDTO {
	    model: string;
	    requestCount: number;
	    errorCount: number;
	    inputTokens: number;
	    outputTokens: number;
	    cacheReadInputTokens: number;
	    cacheCreationInputTokens: number;
	    totalTokens: number;
	    durationMs: number;
	
	    static createFrom(source: any = {}) {
	        return new ModelUsageDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.requestCount = source["requestCount"];
	        this.errorCount = source["errorCount"];
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.cacheReadInputTokens = source["cacheReadInputTokens"];
	        this.cacheCreationInputTokens = source["cacheCreationInputTokens"];
	        this.totalTokens = source["totalTokens"];
	        this.durationMs = source["durationMs"];
	    }
	}
	export class DailyUsageDTO {
	    date: string;
	    requestCount: number;
	    errorCount: number;
	    inputTokens: number;
	    outputTokens: number;
	    cacheReadInputTokens: number;
	    cacheCreationInputTokens: number;
	    totalTokens: number;
	    durationMs: number;
	    models: ModelUsageDTO[];
	
	    static createFrom(source: any = {}) {
	        return new DailyUsageDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.requestCount = source["requestCount"];
	        this.errorCount = source["errorCount"];
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.cacheReadInputTokens = source["cacheReadInputTokens"];
	        this.cacheCreationInputTokens = source["cacheCreationInputTokens"];
	        this.totalTokens = source["totalTokens"];
	        this.durationMs = source["durationMs"];
	        this.models = this.convertValues(source["models"], ModelUsageDTO);
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
	export class ExportResultDTO {
	    path: string;
	    messageCount: number;
	    bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new ExportResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.messageCount = source["messageCount"];
	        this.bytes = source["bytes"];
	    }
	}
	
	export class FileContentDTO {
	    path: string;
	    size: number;
	    content: string;
	    binary: boolean;
	    truncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileContentDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.size = source["size"];
	        this.content = source["content"];
	        this.binary = source["binary"];
	        this.truncated = source["truncated"];
	    }
	}
	export class FileDiffDTO {
	    path: string;
	    oldPath: string;
	    status: string;
	    diff: string;
	    additions: number;
	    deletions: number;
	    binary: boolean;
	    staged: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileDiffDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.oldPath = source["oldPath"];
	        this.status = source["status"];
	        this.diff = source["diff"];
	        this.additions = source["additions"];
	        this.deletions = source["deletions"];
	        this.binary = source["binary"];
	        this.staged = source["staged"];
	    }
	}
	export class FileEntryDTO {
	    name: string;
	    path: string;
	    isDir: boolean;
	    isSymlink: boolean;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new FileEntryDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	        this.isSymlink = source["isSymlink"];
	        this.size = source["size"];
	    }
	}
	export class FileImageDTO {
	    path: string;
	    size: number;
	    mediaType: string;
	    dataBase64: string;
	
	    static createFrom(source: any = {}) {
	        return new FileImageDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.size = source["size"];
	        this.mediaType = source["mediaType"];
	        this.dataBase64 = source["dataBase64"];
	    }
	}
	export class FileListDTO {
	    entries: FileEntryDTO[];
	    truncated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileListDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = this.convertValues(source["entries"], FileEntryDTO);
	        this.truncated = source["truncated"];
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
	export class HookDTO {
	    event: string;
	    matcher: string;
	    command: string;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new HookDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.event = source["event"];
	        this.matcher = source["matcher"];
	        this.command = source["command"];
	        this.source = source["source"];
	    }
	}
	export class HookInput {
	    event: string;
	    matcher: string;
	    command: string;
	    scope: string;
	    originalEvent: string;
	    originalMatcher: string;
	    originalCommand: string;
	    originalScope: string;
	
	    static createFrom(source: any = {}) {
	        return new HookInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.event = source["event"];
	        this.matcher = source["matcher"];
	        this.command = source["command"];
	        this.scope = source["scope"];
	        this.originalEvent = source["originalEvent"];
	        this.originalMatcher = source["originalMatcher"];
	        this.originalCommand = source["originalCommand"];
	        this.originalScope = source["originalScope"];
	    }
	}
	
	export class ImageInput {
	    mediaType?: string;
	    data: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mediaType = source["mediaType"];
	        this.data = source["data"];
	    }
	}
	export class MCPServerDTO {
	    name: string;
	    command: string;
	    args: string[];
	    envKeys: string[];
	    state: string;
	    error?: string;
	    toolCount: number;
	    protocolVersion?: string;
	    serverName?: string;
	    serverVersion?: string;
	
	    static createFrom(source: any = {}) {
	        return new MCPServerDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.envKeys = source["envKeys"];
	        this.state = source["state"];
	        this.error = source["error"];
	        this.toolCount = source["toolCount"];
	        this.protocolVersion = source["protocolVersion"];
	        this.serverName = source["serverName"];
	        this.serverVersion = source["serverVersion"];
	    }
	}
	export class MCPServerInput {
	    name: string;
	    command: string;
	    args: string[];
	    env: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new MCPServerInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.env = source["env"];
	    }
	}
	export class MCPToolDTO {
	    name: string;
	    server: string;
	    tool: string;
	    description: string;
	    inputSchema: any;
	    readOnly: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MCPToolDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.server = source["server"];
	        this.tool = source["tool"];
	        this.description = source["description"];
	        this.inputSchema = source["inputSchema"];
	        this.readOnly = source["readOnly"];
	    }
	}
	export class MemoryDTO {
	    name: string;
	    description: string;
	    type: string;
	    fileName: string;
	    updatedAtMs: number;
	
	    static createFrom(source: any = {}) {
	        return new MemoryDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.type = source["type"];
	        this.fileName = source["fileName"];
	        this.updatedAtMs = source["updatedAtMs"];
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
	export class ModelPresetDTO {
	    id: string;
	    name: string;
	    provider: string;
	    baseUrl: string;
	    defaultModel: string;
	    needsKey: boolean;
	    local: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModelPresetDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.provider = source["provider"];
	        this.baseUrl = source["baseUrl"];
	        this.defaultModel = source["defaultModel"];
	        this.needsKey = source["needsKey"];
	        this.local = source["local"];
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
	export class WorktreeDTO {
	    path: string;
	    branch: string;
	    head: string;
	    detached: boolean;
	    bare: boolean;
	    locked: boolean;
	    prunable: boolean;
	    main: boolean;
	    managed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WorktreeDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.branch = source["branch"];
	        this.head = source["head"];
	        this.detached = source["detached"];
	        this.bare = source["bare"];
	        this.locked = source["locked"];
	        this.prunable = source["prunable"];
	        this.main = source["main"];
	        this.managed = source["managed"];
	    }
	}
	export class ProjectWorktreesDTO {
	    projectPath: string;
	    repoRoot: string;
	    worktreesRoot: string;
	    worktrees: WorktreeDTO[];
	
	    static createFrom(source: any = {}) {
	        return new ProjectWorktreesDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.projectPath = source["projectPath"];
	        this.repoRoot = source["repoRoot"];
	        this.worktreesRoot = source["worktreesRoot"];
	        this.worktrees = this.convertValues(source["worktrees"], WorktreeDTO);
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
	export class ProviderConfigDTO {
	    provider: string;
	    baseUrl: string;
	    model: string;
	    hasApiKey: boolean;
	    apiKeyMasked?: string;
	    contextWindow: number;
	    maxOutputTokens: number;
	    thinkingEnabled: boolean;
	    thinkingBudget: number;
	    permissionMode: string;
	    workDir: string;
	    initError?: string;
	    supportsImages: boolean;
	
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
	        this.thinkingEnabled = source["thinkingEnabled"];
	        this.thinkingBudget = source["thinkingBudget"];
	        this.permissionMode = source["permissionMode"];
	        this.workDir = source["workDir"];
	        this.initError = source["initError"];
	        this.supportsImages = source["supportsImages"];
	    }
	}
	export class ProviderConfigInput {
	    provider: string;
	    baseUrl?: string;
	    model?: string;
	    apiKey?: string;
	    contextWindow?: number;
	    maxOutputTokens?: number;
	    thinkingEnabled: boolean;
	    thinkingBudget?: number;
	
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
	        this.thinkingEnabled = source["thinkingEnabled"];
	        this.thinkingBudget = source["thinkingBudget"];
	    }
	}
	export class RequestTraceDTO {
	    atMs: number;
	    sessionId: string;
	    model: string;
	    inputTokens: number;
	    outputTokens: number;
	    cacheReadInputTokens: number;
	    cacheCreationInputTokens: number;
	    totalTokens: number;
	    durationMs: number;
	    stopReason: string;
	    err?: string;
	
	    static createFrom(source: any = {}) {
	        return new RequestTraceDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.atMs = source["atMs"];
	        this.sessionId = source["sessionId"];
	        this.model = source["model"];
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.cacheReadInputTokens = source["cacheReadInputTokens"];
	        this.cacheCreationInputTokens = source["cacheCreationInputTokens"];
	        this.totalTokens = source["totalTokens"];
	        this.durationMs = source["durationMs"];
	        this.stopReason = source["stopReason"];
	        this.err = source["err"];
	    }
	}
	export class RewindImage {
	    mediaType: string;
	    data: string;
	
	    static createFrom(source: any = {}) {
	        return new RewindImage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mediaType = source["mediaType"];
	        this.data = source["data"];
	    }
	}
	export class RewindPointDTO {
	    messageId: string;
	    preview: string;
	    imageCount: number;
	    timeMs: number;
	
	    static createFrom(source: any = {}) {
	        return new RewindPointDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messageId = source["messageId"];
	        this.preview = source["preview"];
	        this.imageCount = source["imageCount"];
	        this.timeMs = source["timeMs"];
	    }
	}
	export class RewindResultDTO {
	    messageId: string;
	    text: string;
	    images?: RewindImage[];
	
	    static createFrom(source: any = {}) {
	        return new RewindResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messageId = source["messageId"];
	        this.text = source["text"];
	        this.images = this.convertValues(source["images"], RewindImage);
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
	export class RuleDTO {
	    id: string;
	    tool: string;
	    pattern?: string;
	    matchMode: string;
	    effect: string;
	    source: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new RuleDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.tool = source["tool"];
	        this.pattern = source["pattern"];
	        this.matchMode = source["matchMode"];
	        this.effect = source["effect"];
	        this.source = source["source"];
	        this.note = source["note"];
	    }
	}
	export class RuleInput {
	    id?: string;
	    tool: string;
	    pattern?: string;
	    matchMode: string;
	    effect: string;
	    source?: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new RuleInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.tool = source["tool"];
	        this.pattern = source["pattern"];
	        this.matchMode = source["matchMode"];
	        this.effect = source["effect"];
	        this.source = source["source"];
	        this.note = source["note"];
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
	    projectPath?: string;
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
	        this.projectPath = source["projectPath"];
	        this.messages = source["messages"];
	        this.updatedAtMs = source["updatedAtMs"];
	        this.running = source["running"];
	    }
	}
	export class SessionSearchHitDTO {
	    role: string;
	    toolName?: string;
	    line: number;
	    snippet: string;
	    // Go type: time
	    time: any;
	
	    static createFrom(source: any = {}) {
	        return new SessionSearchHitDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.toolName = source["toolName"];
	        this.line = source["line"];
	        this.snippet = source["snippet"];
	        this.time = this.convertValues(source["time"], null);
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
	export class SessionSearchResultDTO {
	    sessionId: string;
	    title: string;
	    workDir: string;
	    // Go type: time
	    updatedAt: any;
	    matchCount: number;
	    matches: SessionSearchHitDTO[];
	
	    static createFrom(source: any = {}) {
	        return new SessionSearchResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessionId = source["sessionId"];
	        this.title = source["title"];
	        this.workDir = source["workDir"];
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	        this.matchCount = source["matchCount"];
	        this.matches = this.convertValues(source["matches"], SessionSearchHitDTO);
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
	export class SkillDTO {
	    name: string;
	    description: string;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new SkillDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.source = source["source"];
	    }
	}
	export class SkillDetailDTO {
	    name: string;
	    description: string;
	    source: string;
	    path: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new SkillDetailDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.source = source["source"];
	        this.path = source["path"];
	        this.content = source["content"];
	    }
	}
	export class SkillInput {
	    name: string;
	    description: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new SkillInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.content = source["content"];
	    }
	}
	export class SlashCommandDTO {
	    name: string;
	    description: string;
	    argumentHint: string;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new SlashCommandDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.argumentHint = source["argumentHint"];
	        this.source = source["source"];
	    }
	}
	export class SubagentTaskDTO {
	    id: string;
	    sessionId: string;
	    description: string;
	    agentType: string;
	    status: string;
	    background: boolean;
	    startedAt: string;
	    finishedAt: string;
	    result?: string;
	    error?: string;
	    turns: number;
	    inputTokens: number;
	    outputTokens: number;
	
	    static createFrom(source: any = {}) {
	        return new SubagentTaskDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.sessionId = source["sessionId"];
	        this.description = source["description"];
	        this.agentType = source["agentType"];
	        this.status = source["status"];
	        this.background = source["background"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.result = source["result"];
	        this.error = source["error"];
	        this.turns = source["turns"];
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	    }
	}
	export class SubagentTypeDTO {
	    name: string;
	    description: string;
	    path?: string;
	
	    static createFrom(source: any = {}) {
	        return new SubagentTypeDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.path = source["path"];
	    }
	}
	export class TerminalInfoDTO {
	    id: string;
	    sessionId: string;
	    workDir: string;
	
	    static createFrom(source: any = {}) {
	        return new TerminalInfoDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.sessionId = source["sessionId"];
	        this.workDir = source["workDir"];
	    }
	}
	
	export class UsageSummaryDTO {
	    from: string;
	    to: string;
	    requestCount: number;
	    errorCount: number;
	    inputTokens: number;
	    outputTokens: number;
	    cacheReadInputTokens: number;
	    cacheCreationInputTokens: number;
	    totalTokens: number;
	    durationMs: number;
	    days: DailyUsageDTO[];
	    models: ModelUsageDTO[];
	
	    static createFrom(source: any = {}) {
	        return new UsageSummaryDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = source["from"];
	        this.to = source["to"];
	        this.requestCount = source["requestCount"];
	        this.errorCount = source["errorCount"];
	        this.inputTokens = source["inputTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.cacheReadInputTokens = source["cacheReadInputTokens"];
	        this.cacheCreationInputTokens = source["cacheCreationInputTokens"];
	        this.totalTokens = source["totalTokens"];
	        this.durationMs = source["durationMs"];
	        this.days = this.convertValues(source["days"], DailyUsageDTO);
	        this.models = this.convertValues(source["models"], ModelUsageDTO);
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

