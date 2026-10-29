export interface paths {
    "/api/status": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Status"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/models": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["ModelConfig"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": components["schemas"]["ModelConfigWrite"];
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["ModelConfig"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/providers/{id}/models": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            models: string[];
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/settings": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Settings"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": components["schemas"]["Settings"];
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Settings"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/scenes": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Scene"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/scenes/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": components["schemas"]["Scene"];
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Scene"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/logs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Log"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/usage": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Usage"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/diagnostics": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Diagnostic"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/model-protocols": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            id: string;
                            label: string;
                            operations: ("complete" | "embeddings" | "listModels")[];
                        }[];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/scenes/{id}/state": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["SceneState"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/budget/recovery": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Restoration accounting status */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            restoreId?: string;
                            held: boolean;
                            /** @enum {string} */
                            restorationCoverage: "none" | "unknown" | "manually_acknowledged";
                        };
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/budget/recovery/acknowledge": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        /** Format: uuid */
                        restoreId: string;
                        /** @enum {string} */
                        acknowledgment: "I_reviewed_accounting_and_accept_incomplete_history";
                    };
                };
            };
            responses: {
                /** @description Manual review acknowledged; historical accounting remains incomplete */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            restoreId?: string;
                            held: boolean;
                            /** @enum {string} */
                            restorationCoverage: "none" | "unknown" | "manually_acknowledged";
                        };
                    };
                };
                /** @description Restoration identifier changed */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            error: string;
                        };
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/budget": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Budget accounting coverage; held amounts are not confirmed invoices */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["BudgetCoverage"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content?: never;
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/budget/external-costs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: {
                    page?: number;
                    pageSize?: number;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Imported administrator-declared receipts; bounded page */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            items: {
                                /** @enum {number} */
                                schemaVersion: 1;
                                revision: number;
                                receipt: {
                                    id: string;
                                    /** @enum {string} */
                                    category: "tool" | "storage" | "compute";
                                    /** @enum {string} */
                                    currency: "USD";
                                    billingDate: string;
                                    periodStart: string;
                                    periodEnd: string;
                                    sourceId: string;
                                    /** @enum {string} */
                                    status: "known";
                                    amountNanoUsd: number;
                                } | {
                                    id: string;
                                    /** @enum {string} */
                                    category: "tool" | "storage" | "compute";
                                    /** @enum {string} */
                                    currency: "USD";
                                    billingDate: string;
                                    periodStart: string;
                                    periodEnd: string;
                                    sourceId: string;
                                    /** @enum {string} */
                                    status: "unknown";
                                };
                                correctionId: string | null;
                                /** Format: date-time */
                                recordedAt: string;
                            }[];
                            total: number;
                            page: number;
                            pageSize: number;
                        };
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            error: string;
                        };
                    };
                };
            };
        };
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        id: string;
                        /** @enum {string} */
                        category: "tool" | "storage" | "compute";
                        /** @enum {string} */
                        currency: "USD";
                        billingDate: string;
                        periodStart: string;
                        periodEnd: string;
                        sourceId: string;
                        /** @enum {string} */
                        status: "known";
                        amountNanoUsd: number;
                    } | {
                        id: string;
                        /** @enum {string} */
                        category: "tool" | "storage" | "compute";
                        /** @enum {string} */
                        currency: "USD";
                        billingDate: string;
                        periodStart: string;
                        periodEnd: string;
                        sourceId: string;
                        /** @enum {string} */
                        status: "unknown";
                    };
                };
            };
            responses: {
                /** @description Idempotent imported receipt */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            /** @enum {number} */
                            schemaVersion: 1;
                            revision: number;
                            receipt: {
                                id: string;
                                /** @enum {string} */
                                category: "tool" | "storage" | "compute";
                                /** @enum {string} */
                                currency: "USD";
                                billingDate: string;
                                periodStart: string;
                                periodEnd: string;
                                sourceId: string;
                                /** @enum {string} */
                                status: "known";
                                amountNanoUsd: number;
                            } | {
                                id: string;
                                /** @enum {string} */
                                category: "tool" | "storage" | "compute";
                                /** @enum {string} */
                                currency: "USD";
                                billingDate: string;
                                periodStart: string;
                                periodEnd: string;
                                sourceId: string;
                                /** @enum {string} */
                                status: "unknown";
                            };
                            correctionId: string | null;
                            /** Format: date-time */
                            recordedAt: string;
                        };
                    };
                };
                /** @description Invalid receipt */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            error: string;
                        };
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            error: string;
                        };
                    };
                };
                /** @description Exact receipt conflict or capacity */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            error: string;
                        };
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/budget/external-costs/corrections": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        id: string;
                        expectedRevision: number;
                        replacement: {
                            id: string;
                            /** @enum {string} */
                            category: "tool" | "storage" | "compute";
                            /** @enum {string} */
                            currency: "USD";
                            billingDate: string;
                            periodStart: string;
                            periodEnd: string;
                            sourceId: string;
                            /** @enum {string} */
                            status: "known";
                            amountNanoUsd: number;
                        } | {
                            id: string;
                            /** @enum {string} */
                            category: "tool" | "storage" | "compute";
                            /** @enum {string} */
                            currency: "USD";
                            billingDate: string;
                            periodStart: string;
                            periodEnd: string;
                            sourceId: string;
                            /** @enum {string} */
                            status: "unknown";
                        };
                        correctionId: string;
                    };
                };
            };
            responses: {
                /** @description Exact-version correction; historical revisions retained */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            /** @enum {number} */
                            schemaVersion: 1;
                            revision: number;
                            receipt: {
                                id: string;
                                /** @enum {string} */
                                category: "tool" | "storage" | "compute";
                                /** @enum {string} */
                                currency: "USD";
                                billingDate: string;
                                periodStart: string;
                                periodEnd: string;
                                sourceId: string;
                                /** @enum {string} */
                                status: "known";
                                amountNanoUsd: number;
                            } | {
                                id: string;
                                /** @enum {string} */
                                category: "tool" | "storage" | "compute";
                                /** @enum {string} */
                                currency: "USD";
                                billingDate: string;
                                periodStart: string;
                                periodEnd: string;
                                sourceId: string;
                                /** @enum {string} */
                                status: "unknown";
                            };
                            correctionId: string | null;
                            /** Format: date-time */
                            recordedAt: string;
                        };
                    };
                };
                /** @description Invalid correction */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            error: string;
                        };
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            error: string;
                        };
                    };
                };
                /** @description Receipt or revision conflict */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            error: string;
                        };
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-people": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            sceneId: string;
                            people: components["schemas"]["MemoryPerson"][];
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Scene unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-people/profile": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    personId: string;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["PersonMemoryProfile"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Scene unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-semantics": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    limit?: number;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Response */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            enabled: boolean;
                            /** @enum {string} */
                            coverage: "complete" | "limited";
                            items: components["schemas"]["SemanticAdministrationItem"][];
                            unavailable: {
                                id: string;
                                version: number;
                                /** @enum {string} */
                                status: "candidate" | "admitted" | "rejected";
                                /** @enum {string} */
                                reason: "qualification_unavailable";
                            }[];
                        };
                    };
                };
                /** @description Response */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Response */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-semantics/{id}/review": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        sceneId: string;
                        expectedVersion: number;
                        /** @enum {string} */
                        status: "admitted" | "rejected";
                        /** @enum {boolean} */
                        confirmed: true;
                    };
                };
            };
            responses: {
                /** @description Response */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            id: string;
                            version: number;
                            /** @enum {string} */
                            status: "candidate" | "admitted" | "rejected";
                        };
                    };
                };
                /** @description Response */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Response */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Response */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Response */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-knowledge/query": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        sceneId: string;
                        personId?: string;
                        seedBackingRef?: {
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        };
                        anchor: {
                            /** @enum {string} */
                            kind: "person";
                            personId: string;
                        } | {
                            /** @enum {string} */
                            kind: "memory";
                            backingRef: {
                                id: string;
                                version: number;
                                deletionEpoch: number;
                            };
                        } | {
                            /** @enum {string} */
                            kind: "entity";
                            entityId: string;
                            backingRef: {
                                id: string;
                                version: number;
                                deletionEpoch: number;
                            };
                        };
                        predicates: ("participates_in" | "prefers_topic" | "uses_tool" | "member_of" | "part_of" | "depends_on" | "has_property")[];
                        depth: 1 | 2;
                    };
                };
            };
            responses: {
                /** @description Response */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["KnowledgeAdministrationView"];
                    };
                };
                /** @description Response */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Response */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Response */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Response */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-identities": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["MemoryIdentityAdministration"];
                    };
                };
                /** @description Success */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        operationId: string;
                        expectedRevision: number;
                        /** @enum {boolean} */
                        confirmed: true;
                        /** @enum {string} */
                        action: "link";
                        left: {
                            adapterId: string;
                            accountId: string;
                            personId: string;
                        };
                        right: {
                            adapterId: string;
                            accountId: string;
                            personId: string;
                        };
                    } | {
                        operationId: string;
                        expectedRevision: number;
                        /** @enum {boolean} */
                        confirmed: true;
                        /** @enum {string} */
                        action: "unlink";
                        left: {
                            adapterId: string;
                            accountId: string;
                            personId: string;
                        };
                        right: {
                            adapterId: string;
                            accountId: string;
                            personId: string;
                        };
                    } | {
                        operationId: string;
                        expectedRevision: number;
                        /** @enum {boolean} */
                        confirmed: true;
                        /** @enum {string} */
                        action: "split";
                        partition: {
                            adapterId: string;
                            accountId: string;
                            personId: string;
                        }[];
                    } | {
                        operationId: string;
                        expectedRevision: number;
                        /** @enum {boolean} */
                        confirmed: true;
                        /** @enum {string} */
                        action: "alias_put";
                        aliasId: string;
                        sceneId: string;
                        endpoint: {
                            adapterId: string;
                            accountId: string;
                            personId: string;
                        };
                        label: string;
                        /** Format: date-time */
                        validFrom: string | null;
                        /** Format: date-time */
                        validUntil: string | null;
                    } | {
                        operationId: string;
                        expectedRevision: number;
                        /** @enum {boolean} */
                        confirmed: true;
                        /** @enum {string} */
                        action: "alias_remove";
                        aliasId: string;
                        sceneId: string;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            revision: number;
                            duplicate: boolean;
                        };
                    };
                };
                /** @description Success */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Success */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Success */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Success */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-relations": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    personId?: string;
                    status?: "candidate" | "accepted" | "rejected";
                    limit?: number;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            sceneId: string;
                            enabled: boolean;
                            items: components["schemas"]["MemoryRelationView"][];
                            /** @enum {string} */
                            coverage: "complete" | "limited";
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Relation or scene unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-relations/{id}/review": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: {
                content: {
                    "application/json": {
                        sceneId: string;
                        expectedVersion: number;
                        /** @enum {string} */
                        status: "accepted" | "rejected";
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["MemoryRelationView"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Relation or scene unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Revision conflict */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-experience": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    status?: "candidate" | "validated" | "active" | "suspended" | "retired";
                    limit?: number;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            enabled: boolean;
                            sceneId: string;
                            items: components["schemas"]["MemoryExperienceView"][];
                            /** @enum {string} */
                            coverage: "complete" | "limited";
                        };
                    };
                };
                /** @description Invalid input */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Experience or scene unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Revision or lifecycle conflict */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-experience/{id}/validate": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: {
                content: {
                    "application/json": {
                        sceneId: string;
                        expectedVersion: number;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            /** @enum {boolean} */
                            ok: true;
                        };
                    };
                };
                /** @description Invalid input */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Experience or scene unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Revision or lifecycle conflict */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-experience/{id}/review": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: {
                content: {
                    "application/json": {
                        sceneId: string;
                        expectedVersion: number;
                        /** @enum {string} */
                        status: "active" | "suspended" | "retired";
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            /** @enum {boolean} */
                            ok: true;
                        };
                    };
                };
                /** @description Invalid input */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Experience or scene unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Revision or lifecycle conflict */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-experience/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        delete: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: {
                content: {
                    "application/json": {
                        sceneId: string;
                        expectedVersion: number;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            /** @enum {boolean} */
                            ok: true;
                        };
                    };
                };
                /** @description Invalid input */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Experience or scene unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Revision or lifecycle conflict */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-history": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    id: string;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            revisions: components["schemas"]["MemoryRevisionView"][];
                            /** @enum {string} */
                            coverage: "complete" | "limited";
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Memory unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Evidence traversal limit reached */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/embedding-index": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["EmbeddingIndexStatus"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/embedding-index/{action}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    action: "start" | "pause" | "cancel" | "resume";
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        /** @default false */
                        confirmUnknownRequest?: boolean;
                        expectedJobId: string | null;
                        expectedEncoderRevision?: string;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["EmbeddingIndexStatus"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/learning/status": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["LearningStatus"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/learning/failures/{id}/retry": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            ok: boolean;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/learning/failures/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        delete: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            ok: boolean;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/habits": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Habit"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/habits/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        delete: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            ok: boolean;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        options?: never;
        head?: never;
        patch: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        locked: boolean;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Habit"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        trace?: never;
    };
    "/api/models/{id}/probe": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        /** @enum {string} */
                        capability: "text" | "tools" | "vision" | "embedding";
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            ok: boolean;
                            /** @enum {string} */
                            capability: "text" | "tools" | "vision" | "embedding";
                            detail: string;
                            durationMs: number;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memes": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: {
                    page?: number;
                    pageSize?: number;
                    query?: string;
                    scope?: string;
                    state?: "*" | "favorite" | "disabled" | "available" | "unanalyzed" | "known";
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            items: components["schemas"]["Meme"][];
                            total: number;
                            page: number;
                            pageSize: number;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memes/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Meme"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            ok: boolean;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        options?: never;
        head?: never;
        patch: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        available?: boolean;
                        shared?: boolean;
                        privacyReviewed?: boolean;
                        favorite?: boolean;
                        disabled?: boolean;
                        tags?: {
                            appearance: string[];
                            style: string[];
                            subject: string[];
                            emotion: string[];
                            intent: string[];
                            tone: string[];
                            intensity: string[];
                            memeMeaning: string[];
                            ocr: string;
                        };
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Meme"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        trace?: never;
    };
    "/api/memes/{id}/thumbnail": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Static first-frame thumbnail, at most 320 pixels per side */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "image/webp": string;
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memes/{id}/file": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Original image bytes, preserving animation */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/octet-stream": string;
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memes/{id}/inclusion": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        included: boolean;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Meme"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memes/{id}/analyze": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        sceneId: string;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Meme"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memes/import": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        sceneId: string;
                        messageId: string;
                        base64: string;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Meme"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/simulations": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            id: string;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/simulations/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Message"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            ok: boolean;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/simulations/{id}/messages": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        text: string;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Message"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/scenes/{id}/messages": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Message"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/scenes/{id}/messages/{messageId}/media/{partIndex}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                    messageId: string;
                    partIndex: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Validated original image bytes, preserving animation */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "image/png": string;
                        "image/jpeg": string;
                        "image/gif": string;
                        "image/webp": string;
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Image unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Invalid or unsupported image */
                415: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/scenes/{id}/history": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            items: components["schemas"]["Message"][];
                            /** @enum {string} */
                            coverage: "complete" | "partial" | "unavailable";
                            nextCursor?: string;
                            reason?: string;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memories": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Memory"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        scope: {
                            /** @enum {string} */
                            kind: "scene";
                            sceneId: string;
                            personId?: string;
                        } | {
                            /** @enum {string} */
                            kind: "sharedPublicKnowledge";
                        };
                        content: string;
                        /** @enum {string} */
                        kind: "fact" | "claim" | "term" | "experience" | "summary";
                        /**
                         * @default medium
                         * @enum {string}
                         */
                        importance?: "low" | "medium" | "high";
                        /** @default 1 */
                        confidence?: number;
                        /** Format: date-time */
                        validFrom?: string;
                        /** Format: date-time */
                        validUntil?: string;
                        /** Format: date-time */
                        occurredAt?: string;
                        /** @enum {string} */
                        epistemicKind?: "explicit" | "reported" | "observed" | "inferred" | "hypothesis";
                        /** @default false */
                        locked?: boolean;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Memory"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memories/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        delete: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            ok: boolean;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        options?: never;
        head?: never;
        patch: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        guard?: {
                            expectedVersion: number;
                            expectedDeletionEpoch: number;
                            idempotencyKey: string;
                        };
                        content?: string;
                        locked?: boolean;
                        /** @enum {string} */
                        status?: "active" | "disputed" | "superseded" | "invalidated";
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Memory"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Success */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        trace?: never;
    };
    "/api/rules": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Rule"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": components["schemas"]["Rule"][];
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Rule"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/persona/versions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            id: string;
                            createdAt: string;
                            content: string;
                        }[];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/plugins": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Plugin"][];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/plugins/{id}/config": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            [key: string]: unknown;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        [key: string]: unknown;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            [key: string]: unknown;
                        };
                    };
                };
                /** @description Invalid plugin configuration with safe field diagnostics */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["PluginConfigurationFailure"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Plugin startup failed with a sanitized diagnostic code */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["PluginStartupFailure"];
                    };
                };
            };
        };
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/plugins/{id}/enabled": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        enabled: boolean;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            ok: boolean;
                        };
                    };
                };
                /** @description Invalid plugin configuration with safe field diagnostics */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["PluginConfigurationFailure"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Plugin startup failed with a sanitized diagnostic code */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["PluginStartupFailure"];
                    };
                };
            };
        };
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/plugins/{id}/actions/{action}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path: {
                    id: string;
                    action: string;
                };
                cookie?: never;
            };
            requestBody: {
                content: {
                    "application/json": {
                        [key: string]: unknown;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": unknown;
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Service unavailable */
                503: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-commitments": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    personId?: string;
                    lifecycle?: "open" | "terminal" | "all";
                    limit?: number;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            sceneId: string;
                            enabled: boolean;
                            items: components["schemas"]["MemoryCommitmentView"][];
                            /** @enum {string} */
                            coverage: "complete" | "limited";
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Unavailable or disabled */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-commitments/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    version: number;
                    deletionEpoch?: number | null;
                };
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["MemoryCommitmentView"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Unavailable or disabled */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-commitments/{id}/history": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    version: number;
                    deletionEpoch?: number | null;
                };
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            revisions: components["schemas"]["MemoryCommitmentView"][];
                            /** @enum {string} */
                            coverage: "limited";
                            /** @enum {string} */
                            interpretation: "previously_recorded_not_current_truth";
                            /** @enum {string} */
                            sourceBodyCoverage: "unavailable";
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Unavailable or disabled */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-commitments/{id}/timeline": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    version: number;
                    deletionEpoch?: number | null;
                    mutationVersion: number;
                    asOfRecorded?: string;
                    limit?: number;
                };
                header?: never;
                path: {
                    id: string;
                };
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            recordedStates: {
                                recordedAt: string;
                                mutationVersion: number;
                                reason: string;
                                activeVersion: number | null;
                                latestVersion: number;
                                /** @enum {string} */
                                status: "available" | "redacted";
                                states: components["schemas"]["MemoryCommitmentView"][];
                            }[];
                            /** @enum {string} */
                            coverage: "limited";
                            reasons: string[];
                            /** @enum {string} */
                            interpretation: "previously_recorded_declaration_state_not_current_truth";
                            /** @enum {string} */
                            sourceBodyCoverage: "unavailable";
                            /** @enum {string} */
                            stateTime: "server_recorded_not_occurred";
                            window: {
                                /** @enum {number} */
                                maxTransitions: 64;
                                firstRecordedAt: string | null;
                                firstMutationVersion: number | null;
                                prunedThroughMutationVersion: number;
                                throughMutationVersion: number;
                            };
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Unavailable or disabled */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-assertion-history": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    memoryId: string;
                    asOfRecorded?: string;
                    validAt?: string;
                    limit?: number;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["MemoryAssertionHistoricalView"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Unavailable or disabled */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-working-set": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    personId?: string;
                    includeCold?: "true" | "false";
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["MemoryWorkingSetView"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Scene or source unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Pin revision or qualification changed */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-episodes": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: {
            parameters: {
                query: {
                    sceneId: string;
                    personId?: string;
                };
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: never;
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["MemoryEpisodesView"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Scene or source unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Pin revision or qualification changed */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-working-set/pin": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: {
                content: {
                    "application/json": {
                        sceneId: string;
                        selection: {
                            ref: {
                                /** @enum {string} */
                                kind: "assertion";
                                id: string;
                                version: number;
                                deletionEpoch: number;
                            } | {
                                /** @enum {string} */
                                kind: "commitment";
                                id: string;
                                version: number;
                                deletionEpoch: number;
                                mutationVersion: number;
                            };
                            dependencyKey: string;
                        };
                        expectedRevision: number;
                        pinned: boolean;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": {
                            /** @enum {number} */
                            schemaVersion: 1;
                            sceneId: string;
                            selection: {
                                ref: {
                                    /** @enum {string} */
                                    kind: "assertion";
                                    id: string;
                                    version: number;
                                    deletionEpoch: number;
                                } | {
                                    /** @enum {string} */
                                    kind: "commitment";
                                    id: string;
                                    version: number;
                                    deletionEpoch: number;
                                    mutationVersion: number;
                                };
                                dependencyKey: string;
                            };
                            policyRevision: number;
                            pinned: boolean;
                            terminal: boolean;
                        };
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Scene or source unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Pin revision or qualification changed */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/memory-working-set/rebuild": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: {
            parameters: {
                query?: never;
                header?: never;
                path?: never;
                cookie?: never;
            };
            requestBody?: {
                content: {
                    "application/json": {
                        sceneId: string;
                        personId?: string;
                    };
                };
            };
            responses: {
                /** @description Success */
                200: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["MemoryWorkingSetView"];
                    };
                };
                /** @description Invalid request */
                400: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Authentication required */
                401: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Scene or source unavailable */
                404: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
                /** @description Pin revision or qualification changed */
                409: {
                    headers: {
                        [name: string]: unknown;
                    };
                    content: {
                        "application/json": components["schemas"]["Error"];
                    };
                };
            };
        };
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        SnowLumaPluginConfig: {
            /**
             * @description Omitted means legacy WebSocket.
             * @default websocket
             * @enum {string}
             */
            transport: "websocket" | "http";
            url: string;
            accountId: string;
            token?: string;
            clearToken?: boolean;
            eventToken?: string;
            clearEventToken?: boolean;
            credentialEnv?: string;
            /**
             * @default 127.0.0.1
             * @enum {string}
             */
            eventHost: "127.0.0.1" | "::1" | "0.0.0.0" | "::";
            /** @description Required for HTTP transport. */
            eventPort?: number;
            /** @default /onebot/events */
            eventPath: string;
            /** @description Optional legacy environment reference; prefer the database-backed event signature secret. */
            eventCredentialEnv?: string;
        };
        TypingDelayPluginConfig: {
            /** @default 1 */
            speedFactor: number;
        };
        Status: {
            name: string;
            startedAt: string;
            uptimeSeconds: number;
            sceneCount: number;
            enabledScenes: number;
            providerCount: number;
            modelCount: number;
            activeTasks: number;
            recentErrors: number;
            vectorVersion: string;
            version: string;
            queuedModelRequests: number;
            runningModelRequests: number;
            adapters: {
                id: string;
                status: string;
            }[];
        };
        Error: {
            error: string;
            requestId?: string;
        };
        ModelConfig: {
            providers: components["schemas"]["Provider"][];
            models: components["schemas"]["ModelInstance"][];
            routes: components["schemas"]["RoleRoute"][];
        };
        Provider: {
            id: string;
            name: string;
            protocol: string;
            /** Format: uri */
            baseUrl: string;
            /** @default  */
            credentialEnv: string;
            credentialConfigured?: boolean;
            /** @default 2 */
            retryCount: number;
            /** @default 30000 */
            timeoutMs: number;
        };
        ModelInstance: {
            id: string;
            displayName: string;
            providerId: string;
            apiModelName: string;
            capabilities: ("text" | "tools" | "vision" | "embedding")[];
            /** @default 4 */
            embeddingBatchSize: number;
            embeddingDimensions?: number;
            inputUsdPerMillion?: number;
            outputUsdPerMillion?: number;
            cacheReadUsdPerMillion?: number;
            cacheWriteUsdPerMillion?: number;
            cacheWrite5mUsdPerMillion?: number;
            cacheWrite1hUsdPerMillion?: number;
            cacheWrite30mUsdPerMillion?: number;
            cacheWrite12hUsdPerMillion?: number;
        };
        RoleRoute: {
            /** @enum {string} */
            role: "planner" | "replyer" | "thinker" | "learning" | "vision" | "embedding";
            orderedModelIds: string[];
            timeoutMs: number;
        };
        ModelConfigWrite: {
            providers: components["schemas"]["Provider"][];
            models: components["schemas"]["ModelInstance"][];
            routes: components["schemas"]["RoleRoute"][];
            credentials?: {
                [key: string]: components["schemas"]["SecretMutation"];
            };
        };
        SecretMutation: {
            /** @enum {string} */
            action: "preserve";
        } | {
            /** @enum {string} */
            action: "clear";
        } | {
            /** @enum {string} */
            action: "set";
            value: string;
        };
        Settings: {
            /** @default false */
            budgetExperimentEnabled: boolean;
            /**
             * @default strict
             * @enum {string}
             */
            budgetMode: "strict" | "best_effort";
            /** @default 1 */
            budgetDailyUsd: number;
            /** @default 0.25 */
            budgetMaxUnresolvedUsd: number;
            /** @default 0.1 */
            budgetPerTurnUsd: number;
            /** @default 32768 */
            budgetPerTurnTokens: number;
            /** @default 8 */
            budgetPerTurnCalls: number;
            /** @default 2048 */
            budgetMaxOutputTokens: number;
            /** @default 0.2 */
            budgetRealtimeRatio: number;
            /** @default 0.1 */
            budgetMaintenanceRatio: number;
            name: string;
            timezone: string;
            persona: string;
            cacheEnabled: boolean;
            /** @default false */
            replySuggestionsEnabled: boolean;
            /** @default 65536 */
            replyEvidenceMaxBytes: number;
            /** @default 131072 */
            plannerEvidenceMaxBytes: number;
            cacheHours: number;
            debounceMs: number;
            maxWaitMs: number;
            /** @default 30000 */
            learningBatchMs: number;
            /** @default false */
            topicAdmissionEnabled: boolean;
            /** @default false */
            sourceEventRevisionsEnabled: boolean;
            /** @default false */
            memoryCommitmentsEnabled: boolean;
            /** @default false */
            memoryCommitmentsAnswerEnabled: boolean;
            /** @default false */
            memoryHistoricalReadEnabled: boolean;
            /** @default false */
            memoryWorkingSetEnabled: boolean;
            /** @default false */
            memoryEpisodesEnabled: boolean;
            /** @default 30 */
            memoryColdAfterDays: number;
            /** @default 14 */
            memoryReviewAfterDays: number;
            /** @default false */
            memoryProjectionEnabled: boolean;
            /** @default false */
            memoryGraphEnabled: boolean;
            /** @default false */
            memoryGraphAutoAdmissionEnabled: boolean;
            /** @default false */
            memoryGraphAnswerEnabled: boolean;
            /** @default false */
            memoryGraphShadowEnabled: boolean;
            /** @default false */
            memorySemanticEnabled: boolean;
            /** @default false */
            memorySemanticAutoAdmissionEnabled: boolean;
            /** @default false */
            memorySemanticAnswerEnabled: boolean;
            /** @default false */
            memoryKnowledgeEnabled: boolean;
            /** @default false */
            memoryKnowledgeAutoAdmissionEnabled: boolean;
            /** @default false */
            memoryKnowledgeAnswerEnabled: boolean;
            /** @default false */
            memoryExperienceEnabled: boolean;
            /** @default false */
            memoryExperienceAutoActivateEnabled: boolean;
            /** @default false */
            memoryExperienceHintsEnabled: boolean;
            /** @default false */
            memoryExperienceFeedbackEnabled: boolean;
            /** @default 120000 */
            learningMaxWaitMs: number;
            /** @default 60000 */
            learningMinIntervalMs: number;
            /** @default 2048 */
            learningQueueMaxMessages: number;
            /** @default 8192 */
            learningQueueMaxTotalMessages: number;
            /** @default 3 */
            learningRetryAttempts: number;
            /** @default 1000 */
            learningRetryBaseMs: number;
            /** @default false */
            debugModelIO: boolean;
            /** @default 4 */
            modelConcurrency: number;
            /** @default [] */
            participationWindows: {
                startHour: number;
                endHour: number;
                /** @enum {string} */
                inclination: "quiet" | "balanced" | "engaged";
            }[];
        };
        Scene: {
            id: string;
            /** @enum {string} */
            kind: "group" | "private";
            adapterId: string;
            accountId: string;
            platformSceneId: string;
            name: string;
            avatarUrl?: string;
            participationEnabled: boolean;
            learningEnabled: boolean;
            memoryReadEnabled?: boolean;
            personalizationEnabled?: boolean;
            memoryReadRevision?: number;
            proactiveEnabled: boolean;
        };
        Log: {
            requestMeasurement?: {
                attempts: number;
                measuredBodyAttempts: number;
                bodyUtf8Bytes: number | null;
                /** @enum {string} */
                bytesCoverage: "complete" | "partial" | "unknown";
                textCountedAttempts: number;
                serializedTextTokens: number | null;
                /** @enum {string} */
                textTokenCoverage: "complete" | "partial" | "unknown";
                tokenizerContracts: string[];
                /** @enum {string} */
                contractCoverage: "complete" | "limited";
                /** @enum {string} */
                providerTokenCount: "unknown";
                /** @enum {string} */
                providerFraming: "uncalibrated";
                /** @enum {boolean} */
                hardTokenCap: false;
            };
            id: string;
            at: string;
            /** @enum {string} */
            level: "info" | "warn" | "error";
            module: string;
            code: string;
            message?: string;
            traceId?: string;
            spanId?: string;
            parentSpanId?: string;
            parentTraceId?: string;
            linkedTraceIds?: string[];
            batchId?: string;
            stage?: string;
            /** @enum {string} */
            status?: "started" | "completed" | "failed" | "skipped" | "cancelled" | "cancel_requested" | "partial";
            reason?: string;
            nextStep?: string;
            counts?: {
                [key: string]: number;
            };
            ruleIds?: string[];
            role?: string;
            apiModel?: string;
            toolName?: string;
            action?: string;
            outputKind?: string;
            receiptState?: string;
            mode?: string;
            operation?: string;
            phase?: string;
            errorType?: string;
            errorCode?: string;
            errorMessage?: string;
            adviceCode?: string;
            errorContext?: {
                operation?: string;
                stage?: string;
                role?: string;
                modelId?: string;
                providerId?: string;
                apiModel?: string;
                attempt?: number;
                maxAttempts?: number;
                stageBudgetMs?: number;
                timeoutMs?: number;
                remainingCandidates?: number;
                retryable?: boolean;
            };
            rootCause?: {
                errorType: string;
                errorCode?: string;
                errorMessage?: string;
                adviceCode?: string;
                errorContext?: {
                    operation?: string;
                    stage?: string;
                    role?: string;
                    modelId?: string;
                    providerId?: string;
                    apiModel?: string;
                    attempt?: number;
                    maxAttempts?: number;
                    stageBudgetMs?: number;
                    timeoutMs?: number;
                    remainingCandidates?: number;
                    retryable?: boolean;
                };
                systemCode?: string;
                httpStatus?: number;
                stackFrames: string[];
            };
            failures?: {
                errorType: string;
                errorCode?: string;
                errorMessage?: string;
                adviceCode?: string;
                errorContext?: {
                    operation?: string;
                    stage?: string;
                    role?: string;
                    modelId?: string;
                    providerId?: string;
                    apiModel?: string;
                    attempt?: number;
                    maxAttempts?: number;
                    stageBudgetMs?: number;
                    timeoutMs?: number;
                    remainingCandidates?: number;
                    retryable?: boolean;
                };
                systemCode?: string;
                httpStatus?: number;
                stackFrames: string[];
            }[];
            stackFrames?: string[];
            causes?: {
                errorType: string;
                errorCode?: string;
                errorMessage?: string;
                adviceCode?: string;
                errorContext?: {
                    operation?: string;
                    stage?: string;
                    role?: string;
                    modelId?: string;
                    providerId?: string;
                    apiModel?: string;
                    attempt?: number;
                    maxAttempts?: number;
                    stageBudgetMs?: number;
                    timeoutMs?: number;
                    remainingCandidates?: number;
                    retryable?: boolean;
                };
                systemCode?: string;
                httpStatus?: number;
                stackFrames: string[];
            }[];
            requestId?: string;
            systemCode?: string;
            httpStatus?: number;
            sceneId?: string;
            turnId?: string;
            modelId?: string;
            providerId?: string;
            durationMs?: number;
        };
        Usage: {
            day: string;
            modelId: string;
            providerId: string;
            apiModelName: string;
            role: string;
            calls: number;
            inputTokens: number;
            outputTokens: number;
            durationMs: number;
            pricedCalls: number;
            estimatedUsd: number;
            ordinaryInputTokens: number;
            cacheReadInputTokens: number;
            cacheWriteInputTokens: number;
            cacheWrite5mInputTokens: number;
            cacheWrite1hInputTokens: number;
            cacheWrite30mInputTokens: number;
            cacheWrite12hInputTokens: number;
            ordinaryInputUsd: number;
            outputUsd: number;
            cacheReadUsd: number;
            cacheWriteUsd: number;
            legacyEstimatedUsd: number;
            cacheKnownCalls: number;
            cacheReadKnownCalls: number;
            cacheWriteKnownCalls: number;
            partialPricedCalls: number;
            legacyCalls: number;
            unknownUsageCalls: number;
        };
        Diagnostic: {
            id: string;
            at: string;
            modelId: string;
            role: string;
            input: string;
            output: string;
        };
        SceneState: {
            activeTasks: number;
            secondsSinceBotSpoke: number | null;
            topicRouting: {
                enabled: boolean;
                deferred: number;
                overflowed: number;
                provisional: number;
                /** @enum {number} */
                maxTopics: 4;
                /** @enum {number} */
                maxDeferred: 60;
            };
            topics: {
                id: string;
                attention: number;
                ageMs: number;
                messageCount: number;
            }[];
        };
        BudgetCoverage: {
            admissionHold: boolean;
            /** @enum {string} */
            restorationCoverage: "none" | "unknown" | "manually_acknowledged";
            restorationId?: string;
            /** @enum {string} */
            currency: "USD";
            windowStart: string;
            knownNanoUsd: number;
            externalKnownNanoUsd: number;
            externalUnknownReceipts: number;
            externalCosts: {
                /** @enum {string} */
                currency: "USD";
                windowStart: string;
                knownTodayNanoUsd: number;
                knownAllTimeNanoUsd: number;
                unknownReceipts: number;
                knownReceipts: number;
                /** @enum {string} */
                coverage: "known_imported_only" | "incomplete";
            };
            heldNanoUsd: number;
            carriedNanoUsd: number;
            unresolvedNanoUsd: number;
            admissionExposureNanoUsd: number;
            remainingNanoUsd: number;
            laneExposureNanoUsd: {
                foreground: number;
                realtime: number;
                maintenance: number;
            };
            overrunAttempts: number;
            unboundedAttempts: number;
            enabled: boolean;
            /** @enum {string} */
            mode: "strict" | "best_effort";
            /** @enum {string} */
            estimateKind: "verified_contract_required" | "best_effort_not_hard_limit";
            /** @enum {string} */
            amountUnit: "nanoUSD";
            /** @enum {number} */
            usdPerNano: 1e-9;
            knownUsd: number;
            heldUsd: number;
            remainingUsd: number;
            partialAttempts: number;
            unknownAttempts: number;
            pendingAttempts: number;
            /** @enum {string} */
            coverage: "settled" | "pending" | "unbounded" | "external_incomplete";
        };
        MemoryPerson: {
            personId: string;
            displayName?: string;
            recordCount: number;
        };
        PersonMemoryProfile: {
            sceneId: string;
            personId: string;
            displayName?: string;
            records: components["schemas"]["PersonMemoryRecord"][];
            /** @enum {string} */
            subjectEvidenceCoverage: "complete" | "limited";
            timeline: {
                memoryId: string;
                recordedAt: string;
                observedAt?: string;
                validFrom?: string;
                occurredAt?: string;
                validUntil?: string;
                /** @enum {string} */
                status: "active" | "disputed" | "superseded" | "invalidated";
                /** @enum {string} */
                origin: "manual" | "learned";
                sourceCount: number;
            }[];
        };
        PersonMemoryRecord: {
            id: string;
            scope: {
                /** @enum {string} */
                kind: "scene";
                sceneId: string;
                personId: string;
            };
            /** @enum {string} */
            kind: "fact" | "claim" | "term" | "experience" | "summary";
            content: string;
            sources: {
                sceneId: string;
                messageId: string;
                senderId: string;
            }[];
            /** @enum {string} */
            importance: "low" | "medium" | "high";
            confidence: number;
            learnedAt: string;
            observedAt?: string;
            subjectEvidence?: {
                observedAt?: string;
                /** @enum {string} */
                coverage: "complete" | "limited";
                witnesses: {
                    /** @enum {string} */
                    kind: "self" | "mentioned" | "legacy_sender" | "unresolved";
                    personId?: string;
                    subjectDisplayName?: string;
                    sourceMessageId?: string;
                    mentionPartIndex?: number;
                    sources: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                        senderDisplayName?: string;
                    }[];
                    observations: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                        sentAt?: string;
                        receivedAt?: string;
                    }[];
                }[];
            };
            validFrom?: string;
            /** @enum {string} */
            epistemicKind?: "explicit" | "reported" | "observed" | "inferred" | "hypothesis";
            occurredAt?: string;
            validUntil?: string;
            /** @enum {string} */
            status: "active" | "disputed" | "superseded" | "invalidated";
            /** @enum {string} */
            origin: "manual" | "learned";
            locked: boolean;
            supersedesId?: string;
            /** @enum {string} */
            lineageStatus?: "verified" | "unverified" | "quarantined";
        };
        SemanticAdministrationItem: {
            id: string;
            version: number;
            personId: string | null;
            /** @enum {string} */
            kind: "summary_candidate" | "duplicate_candidate" | "episode_candidate";
            text: string;
            selectedParentIds: string[];
            /** @enum {string} */
            status: "candidate" | "admitted" | "rejected";
            admission: string;
            hypothesis: boolean;
            anchorQualifications: {
                edgeId: string;
                edgeVersion: number;
                /** @enum {number} */
                registryVersion: 1;
                /** @enum {string} */
                predicate: "participates_in" | "prefers_topic" | "uses_tool" | "member_of" | "part_of" | "depends_on" | "has_property";
                /** @enum {string} */
                property?: "version" | "count" | "duration" | "enabled";
                subject: {
                    /** @enum {string} */
                    kind: "person";
                    personId: string;
                } | {
                    /** @enum {string} */
                    kind: "entity";
                    entityId: string;
                    /** @enum {string} */
                    entityKind: "plan" | "event" | "topic" | "tool" | "organization" | "concept" | "task";
                    backing: {
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    };
                    originEdgeId: string;
                } | {
                    /** @enum {string} */
                    kind: "literal";
                    /** @enum {string} */
                    valueType: "text";
                    value: string;
                } | {
                    /** @enum {string} */
                    kind: "literal";
                    /** @enum {string} */
                    valueType: "number";
                    value: number;
                    unit: string | null;
                } | {
                    /** @enum {string} */
                    kind: "literal";
                    /** @enum {string} */
                    valueType: "boolean";
                    value: boolean;
                };
                object: {
                    /** @enum {string} */
                    kind: "person";
                    personId: string;
                } | {
                    /** @enum {string} */
                    kind: "entity";
                    entityId: string;
                    /** @enum {string} */
                    entityKind: "plan" | "event" | "topic" | "tool" | "organization" | "concept" | "task";
                    backing: {
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    };
                    originEdgeId: string;
                } | {
                    /** @enum {string} */
                    kind: "literal";
                    /** @enum {string} */
                    valueType: "text";
                    value: string;
                } | {
                    /** @enum {string} */
                    kind: "literal";
                    /** @enum {string} */
                    valueType: "number";
                    value: number;
                    unit: string | null;
                } | {
                    /** @enum {string} */
                    kind: "literal";
                    /** @enum {string} */
                    valueType: "boolean";
                    value: boolean;
                };
                /** @enum {string} */
                polarity: "positive" | "negative";
                /** @enum {string} */
                modality: "asserted" | "possible";
                /** @enum {string} */
                temporalScope: "current" | "past" | "future" | "unspecified";
                conditions: string[];
                /** Format: date-time */
                occurredAt: string | null;
                /** Format: date-time */
                validFrom: string | null;
                /** Format: date-time */
                validUntil: string | null;
                /** @enum {string} */
                status: "candidate" | "accepted";
                /** @enum {string} */
                admission: "qualified_candidate" | "self_explicit_no_visible_conflict" | "direct_assertion_no_visible_conflict";
                /** @enum {string} */
                interpretation: "attributed_proposed_association_not_observed_event";
            }[];
            createdAt: string;
            applicableUntil: string | null;
            episodeAnchor?: {
                id: string;
                version: number;
            };
            parents: {
                ref: {
                    id: string;
                    version: number;
                    deletionEpoch: number;
                };
                proofs: {
                    sources: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                    }[];
                    parents: string[];
                    parentReferences?: {
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    }[];
                    subjectWitness?: {
                        kind: string;
                        personId?: string;
                        sourceMessageId?: string;
                        mentionPartIndex?: number;
                    };
                    sourceObservations?: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                        sentAt?: string;
                        receivedAt?: string;
                    }[];
                }[];
            }[];
            declarations: {
                ref: {
                    id: string;
                    version: number;
                    deletionEpoch: number;
                };
                record: {
                    id: string;
                    content: string;
                    kind: string;
                    epistemicKind?: string;
                    status: string;
                    origin: string;
                    scope: {
                        kind: string;
                        sceneId?: string;
                        personId?: string;
                    };
                    learnedAt: string;
                    observedAt?: string;
                    occurredAt?: string;
                    validFrom?: string;
                    validUntil?: string;
                    sources: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                    }[];
                    internalUse?: string;
                };
            }[];
            rootCount: number;
            /** @enum {string} */
            applicability: "current" | "review_needed";
            interpretation: string;
        };
        KnowledgeAdministrationView: {
            enabled: boolean;
            edges: {
                id: string;
                version: number;
                /** @enum {string} */
                predicate: "participates_in" | "prefers_topic" | "uses_tool" | "member_of" | "part_of" | "depends_on" | "has_property";
                property?: string;
                subject: {
                    /** @enum {string} */
                    kind: "person";
                    personId: string;
                } | {
                    /** @enum {string} */
                    kind: "entity";
                    entity: {
                        id: string;
                        sceneId: string;
                        kind: string;
                        title: string;
                        backing: {
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        };
                    };
                } | {
                    /** @enum {string} */
                    kind: "literal";
                    /** @enum {string} */
                    valueType: "text" | "number" | "boolean";
                    value: string | number | boolean;
                    unit?: string | null;
                };
                object: {
                    /** @enum {string} */
                    kind: "person";
                    personId: string;
                } | {
                    /** @enum {string} */
                    kind: "entity";
                    entity: {
                        id: string;
                        sceneId: string;
                        kind: string;
                        title: string;
                        backing: {
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        };
                    };
                } | {
                    /** @enum {string} */
                    kind: "literal";
                    /** @enum {string} */
                    valueType: "text" | "number" | "boolean";
                    value: string | number | boolean;
                    unit?: string | null;
                };
                /** @enum {string} */
                polarity: "positive" | "negative";
                /** @enum {string} */
                modality: "asserted" | "possible";
                /** @enum {string} */
                temporalScope: "current" | "past" | "future" | "unspecified";
                conditions: string[];
                validFrom?: string;
                validUntil?: string;
                createdAt: string;
                backing: {
                    id: string;
                    version: number;
                    deletionEpoch: number;
                };
                backingStatus: string;
                /** @enum {string} */
                status: "candidate" | "accepted";
                admission: string;
                applicability: string;
                proofs: {
                    sources: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                    }[];
                    parents: string[];
                    parentReferences?: {
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    }[];
                }[];
                dependencies: {
                    entity: {
                        id: string;
                        sceneId: string;
                        kind: string;
                        title: string;
                        backing: {
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        };
                    };
                    proofs: {
                        sources: {
                            sceneId: string;
                            messageId: string;
                            senderId: string;
                        }[];
                        parents: string[];
                        parentReferences?: {
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        }[];
                    }[];
                }[];
                requiresPersonalization: boolean;
                readDependencies: {
                    backing: {
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    };
                    proofs: {
                        sources: {
                            sceneId: string;
                            messageId: string;
                            senderId: string;
                        }[];
                        parents: string[];
                        parentReferences?: {
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        }[];
                    }[];
                    edgeId?: string;
                    edgeVersion?: number;
                }[];
                dependencyStatuses: {
                    entityId: string;
                    status: string;
                }[];
                spans: {
                    source: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                    };
                    partIndex: number;
                    start: number;
                    end: number;
                    stance: string;
                }[];
            }[];
            supportClosure: {
                edges: {
                    id: string;
                    version: number;
                    /** @enum {string} */
                    predicate: "participates_in" | "prefers_topic" | "uses_tool" | "member_of" | "part_of" | "depends_on" | "has_property";
                    property?: string;
                    subject: {
                        /** @enum {string} */
                        kind: "person";
                        personId: string;
                    } | {
                        /** @enum {string} */
                        kind: "entity";
                        entity: {
                            id: string;
                            sceneId: string;
                            kind: string;
                            title: string;
                            backing: {
                                id: string;
                                version: number;
                                deletionEpoch: number;
                            };
                        };
                    } | {
                        /** @enum {string} */
                        kind: "literal";
                        /** @enum {string} */
                        valueType: "text" | "number" | "boolean";
                        value: string | number | boolean;
                        unit?: string | null;
                    };
                    object: {
                        /** @enum {string} */
                        kind: "person";
                        personId: string;
                    } | {
                        /** @enum {string} */
                        kind: "entity";
                        entity: {
                            id: string;
                            sceneId: string;
                            kind: string;
                            title: string;
                            backing: {
                                id: string;
                                version: number;
                                deletionEpoch: number;
                            };
                        };
                    } | {
                        /** @enum {string} */
                        kind: "literal";
                        /** @enum {string} */
                        valueType: "text" | "number" | "boolean";
                        value: string | number | boolean;
                        unit?: string | null;
                    };
                    /** @enum {string} */
                    polarity: "positive" | "negative";
                    /** @enum {string} */
                    modality: "asserted" | "possible";
                    /** @enum {string} */
                    temporalScope: "current" | "past" | "future" | "unspecified";
                    conditions: string[];
                    validFrom?: string;
                    validUntil?: string;
                    createdAt: string;
                    backing: {
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    };
                    backingStatus: string;
                    /** @enum {string} */
                    status: "candidate" | "accepted";
                    admission: string;
                    applicability: string;
                    proofs: {
                        sources: {
                            sceneId: string;
                            messageId: string;
                            senderId: string;
                        }[];
                        parents: string[];
                        parentReferences?: {
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        }[];
                    }[];
                    dependencies: {
                        entity: {
                            id: string;
                            sceneId: string;
                            kind: string;
                            title: string;
                            backing: {
                                id: string;
                                version: number;
                                deletionEpoch: number;
                            };
                        };
                        proofs: {
                            sources: {
                                sceneId: string;
                                messageId: string;
                                senderId: string;
                            }[];
                            parents: string[];
                            parentReferences?: {
                                id: string;
                                version: number;
                                deletionEpoch: number;
                            }[];
                        }[];
                    }[];
                    requiresPersonalization: boolean;
                    readDependencies: {
                        backing: {
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        };
                        proofs: {
                            sources: {
                                sceneId: string;
                                messageId: string;
                                senderId: string;
                            }[];
                            parents: string[];
                            parentReferences?: {
                                id: string;
                                version: number;
                                deletionEpoch: number;
                            }[];
                        }[];
                        edgeId?: string;
                        edgeVersion?: number;
                    }[];
                    dependencyStatuses: {
                        entityId: string;
                        status: string;
                    }[];
                    spans: {
                        source: {
                            sceneId: string;
                            messageId: string;
                            senderId: string;
                        };
                        partIndex: number;
                        start: number;
                        end: number;
                        stance: string;
                    }[];
                }[];
                records: {
                    ref: {
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    };
                    record: {
                        id: string;
                        content: string;
                        kind: string;
                        epistemicKind?: string;
                        status: string;
                        origin: string;
                        learnedAt: string;
                        observedAt?: string;
                        occurredAt?: string;
                        validFrom?: string;
                        validUntil?: string;
                        /** @enum {string} */
                        internalUse?: "commitment_declaration" | "knowledge_declaration";
                        scope: {
                            kind: string;
                            sceneId?: string;
                            personId?: string;
                        };
                        sources: {
                            sceneId: string;
                            messageId: string;
                            senderId: string;
                        }[];
                    };
                }[];
            };
            paths: string[][];
            /** @enum {string} */
            coverage: "complete" | "limited";
            reason?: string;
            /** @enum {string} */
            interpretation: "attributed_claims_not_transitive_truth";
            /** @enum {string} */
            sourceBodyCoverage: "unavailable";
        };
        MemoryIdentityAdministration: {
            revision: number;
            edges: [
                {
                    adapterId: string;
                    accountId: string;
                    personId: string;
                },
                {
                    adapterId: string;
                    accountId: string;
                    personId: string;
                }
            ][];
            aliases: {
                id: string;
                endpoint: {
                    adapterId: string;
                    accountId: string;
                    personId: string;
                };
                label: string;
                scopeSceneId: string;
                source: {
                    /** @enum {string} */
                    kind: "manual";
                    administratorId: string;
                } | {
                    /** @enum {string} */
                    kind: "observation";
                    sceneId: string;
                    messageId: string;
                    senderId: string;
                    eventRef?: {
                        eventId: string;
                        revision: number;
                        deletionEpoch: number;
                    };
                };
                /** Format: date-time */
                validFrom: string | null;
                /** Format: date-time */
                validUntil: string | null;
            }[];
        };
        MemoryRelationView: {
            id: string;
            version: number;
            assertionContent: string;
            personId: string;
            personName?: string;
            /** @enum {string} */
            predicate: "participates_in";
            target: {
                id: string;
                /** @enum {string} */
                kind: "plan" | "event";
                title: string;
            };
            /** @enum {string} */
            polarity: "positive" | "negative";
            /** @enum {string} */
            modality: "asserted" | "possible";
            /** @enum {string} */
            temporalScope: "current" | "past" | "future" | "unspecified";
            conditions: string[];
            occurredAt?: string;
            validFrom?: string;
            validUntil?: string;
            /** @enum {string} */
            epistemicKind?: "explicit" | "reported" | "observed" | "inferred" | "hypothesis";
            /** @enum {string} */
            status: "candidate" | "accepted" | "rejected";
            /** @enum {string} */
            admission: "automatic" | "candidate" | "reviewed";
            /** @enum {string} */
            autoReason?: "self_explicit_no_visible_conflict";
            createdAt: string;
            reviewedAt?: string;
            sourceGroups: {
                /** @enum {string} */
                witnessKind?: "self" | "mentioned" | "legacy_sender" | "unresolved";
                sources: {
                    messageId: string;
                    senderId: string;
                    senderName?: string;
                }[];
                parentCount: number;
            }[];
        };
        MemoryExperienceView: {
            id: string;
            version: number;
            /** @enum {string} */
            action: "exact_person_before_semantic" | "verify_plan_qualifiers" | "conclusion_before_detail";
            condition: {
                /** @enum {string} */
                kind: "person_question";
                personId: string;
            } | {
                /** @enum {string} */
                kind: "plan_question";
            } | {
                /** @enum {string} */
                kind: "response_style";
                personId: string;
            };
            personName?: string;
            trialEligible: boolean;
            trial?: {
                version: number;
                policyVersion: number;
                evidencePolicyVersion: number;
                /** @enum {string} */
                phase: "trial" | "promoted" | "suspended" | "expired";
                requesterName?: string;
                createdAt: string;
                expiresAt: string;
                positiveCount: number;
                instruction: {
                    id: string;
                    version: number;
                };
                /** @enum {string} */
                reason?: "explicit_instruction_trial" | "user_reported_helpfulness" | "negative_feedback" | "instruction_withdrawn" | "expiry" | "qualification_changed";
            };
            /** @enum {string} */
            activationBasis?: "explicit_preference" | "administrator_review";
            /** @enum {string} */
            status: "candidate" | "validated" | "active" | "suspended" | "retired";
            createdAt: string;
            expiresAt: string;
            expired: boolean;
            sources: {
                messageId: string;
                senderId: string;
                senderName?: string;
                observedAt: string;
                /** @enum {string} */
                code: "request_exact_person_lookup" | "request_verify_plan_qualifiers" | "request_conclusion_first" | "counterexample" | "withdraw_instruction" | "withdraw_person_instruction" | "withdraw_plan_instruction" | "user_reported_helpful";
            }[];
            validation?: {
                /** @enum {string} */
                tier: "mechanical_only";
                checks: string[];
                failures: string[];
                passed: boolean;
            };
            uses: {
                exposed: number;
                applied: number;
                declined: number;
            };
        };
        MemoryRevisionView: {
            /** @enum {string} */
            sourceBodyCoverage: "unavailable";
            version: number;
            recordedAt: string | null;
            validFrom: string | null;
            validTo: string | null;
            content: string;
            /** @enum {string} */
            status: "active" | "disputed" | "superseded" | "invalidated";
            /** @enum {string} */
            kind: "fact" | "claim" | "term" | "experience" | "summary";
            /** @enum {string} */
            epistemicKind?: "explicit" | "reported" | "observed" | "inferred" | "hypothesis";
            occurredAt?: string;
            /** @enum {string} */
            origin: "manual" | "learned";
            /** @enum {string} */
            parentBinding: "bound" | "legacy_unknown" | "none";
        };
        EmbeddingIndexStatus: {
            /** @enum {string} */
            state: "unconfigured" | "needs_rebuild" | "ready";
            selected?: {
                modelId: string;
                encoderRevision: string;
                requestedDimensions?: number;
            };
            active?: {
                generationId: string;
                encoderRevision: string;
                modelId: string;
                dimensions: number;
                spaceId: string;
            };
            coverage: {
                eligible: number;
                indexed: number;
                pending: number;
            };
            retained: {
                generations: number;
                vectors: number;
            };
            job?: {
                id: string;
                encoderRevision: string;
                modelId: string;
                /** @enum {string} */
                state: "running" | "paused" | "failed" | "cancelled" | "completed";
                processed: number;
                total: number;
                dimensions?: number;
                spaceId?: string;
                error?: string;
                errorRequestId?: string;
                requiresRetryConfirmation: boolean;
                expectedActive?: string;
                attemptId?: string;
                ownerId?: string;
                leaseUntil?: number;
            };
        };
        LearningStatus: {
            accepted: number;
            completed: number;
            failed: number;
            overflow: number;
            retried: number;
            queued: number;
            retrying: number;
            active: number;
            paused: number;
            failures: {
                id: string;
                sceneId: string;
                attempt: number;
                messageCount: number;
                partial: boolean;
                canRetry: boolean;
                createdAt: string;
            }[];
        };
        Habit: {
            id: string;
            /** @enum {string} */
            kind: "sceneRule" | "botExpression";
            sceneId?: string;
            content?: string;
            preference?: string;
            /** @enum {string} */
            context?: "technical";
            /** @enum {number} */
            evidenceVersion?: 2;
            sourceCutoff?: number;
            authorWatermarks?: {
                [key: string]: number;
            };
            weight: number;
            confidence: number;
            /** @enum {string} */
            status: "active" | "disputed" | "invalidated";
            locked: boolean;
            learnedAt: string;
            sources: {
                sceneId: string;
                messageId: string;
                senderId: string;
                direction: number;
                timestamp?: number;
                authorKey?: string;
            }[];
        };
        Meme: {
            id: string;
            format: string;
            bytes: number;
            width: number;
            height: number;
            frames: number;
            sources: {
                sceneId: string;
                messageId: string;
                senderId: string;
            }[];
            tags: {
                appearance: string[];
                style: string[];
                subject: string[];
                emotion: string[];
                intent: string[];
                tone: string[];
                intensity: string[];
                memeMeaning: string[];
                ocr: string;
            };
            tagRevision?: number;
            analyzed: boolean;
            knownOnly?: boolean;
            contentHash?: string;
            available: boolean;
            shared: boolean;
            privacyReviewed: boolean;
            /** @enum {string} */
            selectionOrigin?: "automatic" | "manual";
            /** @enum {string} */
            sharingOrigin?: "automatic" | "manual";
            favorite: boolean;
            disabled: boolean;
            preference: number;
            usageCount: number;
            lastUsedAt?: string;
            /** @enum {string} */
            analysisState?: "running" | "retrying" | "succeeded" | "failed" | "cancelled" | "interrupted";
            analysisAttempts?: number;
            analysisNextRetryAt?: string;
            analysisFailureCode?: string;
            createdAt: string;
            analysisError?: string;
        };
        Message: {
            id: string;
            sceneId: string;
            senderId: string;
            senderDisplayName?: string;
            senderAvatarUrl?: string;
            senderPlatformId?: string;
            sentAt: string;
            receivedAt: string;
            /** @enum {string} */
            origin: "live" | "history" | "simulation";
            parts: {
                /** @enum {string} */
                type: "text" | "mention" | "media" | "unsupported";
                text?: string;
                userId?: string;
                assetId?: string;
                mediaType?: string;
                mediaUrl?: string;
                /** @enum {string} */
                mediaKind?: "image" | "sticker";
                description?: string;
            }[];
            replyToId?: string;
            /** @enum {string} */
            status: "available" | "recalled" | "unavailable";
        };
        Memory: {
            scope: {
                /** @enum {string} */
                kind: "scene";
                sceneId: string;
                personId?: string;
            } | {
                /** @enum {string} */
                kind: "sharedPublicKnowledge";
            };
            content: string;
            /** @enum {string} */
            kind: "fact" | "claim" | "term" | "experience" | "summary";
            /**
             * @default medium
             * @enum {string}
             */
            importance: "low" | "medium" | "high";
            /** @default 1 */
            confidence: number;
            /** Format: date-time */
            validFrom?: string;
            /** Format: date-time */
            validUntil?: string;
            /** Format: date-time */
            occurredAt?: string;
            /** @enum {string} */
            epistemicKind?: "explicit" | "reported" | "observed" | "inferred" | "hypothesis";
            /** @default false */
            locked: boolean;
            id: string;
            revision?: {
                version: number;
                deletionEpoch: number;
            };
            sources: {
                sceneId: string;
                messageId: string;
                senderId: string;
            }[];
            learnedAt: string;
            observedAt?: string;
            /** @enum {string} */
            status: "active" | "disputed" | "superseded" | "invalidated";
            /** @enum {string} */
            origin: "manual" | "learned";
            /** @enum {string} */
            lineageStatus?: "verified" | "unverified" | "quarantined";
        };
        Rule: {
            id: string;
            /** @enum {string} */
            scope: "global" | "scene";
            sceneId?: string;
            content: string;
            nonOverridable: boolean;
            enabled: boolean;
        };
        Plugin: {
            id: string;
            name: string;
            description: string;
            version: string;
            capabilities: string[];
            configSchema: {
                [key: string]: unknown;
            };
            builtin: boolean;
            configuredSecrets: {
                [key: string]: boolean;
            };
            enabled: boolean;
            error?: string;
            errorRequestId?: string;
            pages: {
                id: string;
                title: string;
                entry: string;
            }[];
        };
        PluginConfigurationFailure: {
            /** @enum {string} */
            error: "invalid_plugin_configuration";
            requestId: string;
            issues: {
                /** @enum {string} */
                field: "configuration" | "transport" | "url" | "accountId" | "token" | "clearToken" | "credentialEnv" | "eventToken" | "clearEventToken" | "eventHost" | "eventPort" | "eventPath" | "eventCredentialEnv";
                /** @enum {string} */
                reason: "required" | "invalid_type" | "invalid_value" | "invalid_url" | "websocket_scheme_required" | "http_scheme_required" | "url_credentials_forbidden" | "url_query_forbidden" | "url_fragment_forbidden" | "invalid_event_path" | "invalid_port" | "conflicting_credential_change";
            }[];
        };
        PluginStartupFailure: {
            /** @enum {string} */
            error: "secret_key_unavailable" | "secret_key_missing" | "secret_key_permissions" | "secret_key_invalid" | "secret_decryption_failed" | "plugin_setup_failed" | "plugin_disabled" | "invalid_plugin_configuration" | "credential_not_configured" | "event_credential_not_configured" | "account_mismatch" | "connection_failed" | "connection_closed" | "request_timeout" | "request_cancelled" | "adapter_not_connected" | "adapter_disposed" | "invalid_websocket_url" | "invalid_http_url" | "invalid_http_event_configuration" | "http_event_listen_failed" | "http_action_failed" | "http_request_failed" | "invalid_http_response" | "http_response_too_large" | "invalid_payload" | "invalid_identifier" | "unsupported" | "invalid_action_parameters" | "platform_action_failed";
            requestId?: string;
            systemCode?: string;
            httpStatus?: number;
        };
        MemoryCommitmentView: {
            ref: {
                id: string;
                version: number;
                deletionEpoch: number;
            };
            mutationVersion: number;
            terms?: {
                /** @enum {string} */
                kind: "plan";
                title: string;
                participantIds: string[];
                /** @enum {string} */
                temporalScope: "current" | "past" | "future" | "unspecified";
                /** @enum {string} */
                polarity: "positive" | "negative";
                /** @enum {string} */
                modality: "asserted" | "possible";
                conditions: string[];
                timeQualification?: string;
                /** Format: date-time */
                occurredAt?: string;
                /** Format: date-time */
                validFrom?: string;
                /** Format: date-time */
                validUntil?: string;
            };
            recordedAt?: string;
            /** @enum {string} */
            confirmation: "proposed" | "confirmed" | "incomplete";
            /** @enum {string} */
            lifecycle: "pending" | "done" | "cancelled" | "needs_review";
            /** @enum {string} */
            qualification: "declared_not_verified";
            /** @enum {string} */
            sourceBodyCoverage: "unavailable";
            changed: boolean;
            participants: {
                personId: string;
                displayName?: string;
                confirmed: boolean;
                missing: boolean;
                completed: boolean;
                cancelled: boolean;
            }[];
        };
        MemoryAssertionHistoricalView: {
            /** @enum {string} */
            interpretation: "previously_recorded_not_current_truth";
            /** @enum {string} */
            sourceBodyCoverage: "unavailable";
            asOfRecorded?: string;
            validAt?: string;
            coverage: {
                /** @enum {string} */
                status: "limited" | "retained_scope_only";
                returned: number;
                hasMore: boolean;
            };
            revisions: {
                version: number;
                recordedAt: string | null;
                validFrom: string | null;
                validTo: string | null;
                content: string;
                /** @enum {string} */
                status: "active" | "disputed" | "superseded" | "invalidated";
                kind: string;
                epistemicKind?: string;
                /** @enum {string} */
                validityCoverage: "unknown" | "bounded";
            }[];
        };
        MemoryWorkingSetView: {
            policies: {
                items: {
                    /** @enum {number} */
                    schemaVersion: 1;
                    sceneId: string;
                    selection: {
                        ref: {
                            /** @enum {string} */
                            kind: "assertion";
                            id: string;
                            version: number;
                            deletionEpoch: number;
                        } | {
                            /** @enum {string} */
                            kind: "commitment";
                            id: string;
                            version: number;
                            deletionEpoch: number;
                            mutationVersion: number;
                        };
                        dependencyKey: string;
                    };
                    policyRevision: number;
                    pinned: boolean;
                    terminal: boolean;
                }[];
                /** @enum {string} */
                coverage: "complete" | "limited";
            };
            enabled: boolean;
            /** @enum {string} */
            coverage: "complete" | "limited" | "disabled";
            omittedCold: number;
            omittedEntries?: number;
            items: ({
                /** @enum {string} */
                kind: "assertion";
                record: {
                    id: string;
                    content: string;
                    kind: string;
                    epistemicKind?: string;
                    status: string;
                    learnedAt: string;
                    occurredAt?: string;
                    validFrom?: string;
                    validUntil?: string;
                    sources: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                    }[];
                };
                accessibility: {
                    ref: {
                        /** @enum {string} */
                        kind: "assertion";
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    } | {
                        /** @enum {string} */
                        kind: "commitment";
                        id: string;
                        version: number;
                        deletionEpoch: number;
                        mutationVersion: number;
                    };
                    dependencyKey: string;
                    /** @enum {string} */
                    state: "unknown" | "hot" | "cold";
                    reviewDue: boolean;
                    pinned: boolean;
                    pinIntent: boolean;
                    pinRevision: number;
                    /** @enum {string} */
                    pinStatus: "none" | "current" | "stale" | "terminal";
                    /** @enum {string} */
                    policyCoverage: "complete" | "unknown";
                    metadataRevision: number;
                    contextSelectedCount: number;
                    lastSelectedAt: number | null;
                    /** @enum {string|null} */
                    protection: "open_commitment" | "needs_review_commitment" | "unresolved_commitment_coverage" | null;
                };
            } | {
                /** @enum {string} */
                kind: "commitment";
                view: {
                    sourceMessageIds: string[];
                    terms?: {
                        /** @enum {string} */
                        kind: "plan";
                        title: string;
                        participantIds: string[];
                        /** @enum {string} */
                        temporalScope: "current" | "past" | "future" | "unspecified";
                        /** @enum {string} */
                        polarity: "positive" | "negative";
                        /** @enum {string} */
                        modality: "asserted" | "possible";
                        conditions: string[];
                        timeQualification?: string;
                        /** Format: date-time */
                        occurredAt?: string;
                        /** Format: date-time */
                        validFrom?: string;
                        /** Format: date-time */
                        validUntil?: string;
                    };
                    recordedAt?: string;
                    /** @enum {string} */
                    confirmation: "proposed" | "confirmed" | "incomplete";
                    /** @enum {string} */
                    lifecycle: "pending" | "done" | "cancelled" | "needs_review";
                    /** @enum {string} */
                    qualification: "declared_not_verified";
                    /** @enum {string} */
                    sourceBodyCoverage: "unavailable";
                    confirmedParticipantIds: string[];
                    missingParticipantIds: string[];
                    completedParticipantIds: string[];
                    cancelledParticipantIds: string[];
                };
                accessibility: {
                    ref: {
                        /** @enum {string} */
                        kind: "assertion";
                        id: string;
                        version: number;
                        deletionEpoch: number;
                    } | {
                        /** @enum {string} */
                        kind: "commitment";
                        id: string;
                        version: number;
                        deletionEpoch: number;
                        mutationVersion: number;
                    };
                    dependencyKey: string;
                    /** @enum {string} */
                    state: "unknown" | "hot" | "cold";
                    reviewDue: boolean;
                    pinned: boolean;
                    pinIntent: boolean;
                    pinRevision: number;
                    /** @enum {string} */
                    pinStatus: "none" | "current" | "stale" | "terminal";
                    /** @enum {string} */
                    policyCoverage: "complete" | "unknown";
                    metadataRevision: number;
                    contextSelectedCount: number;
                    lastSelectedAt: number | null;
                    /** @enum {string|null} */
                    protection: "open_commitment" | "needs_review_commitment" | "unresolved_commitment_coverage" | null;
                };
            })[];
        };
        MemoryEpisodesView: {
            enabled: boolean;
            /** @enum {string} */
            interpretation: "independent_qualified_records_not_synthesized_episodes";
            /** @enum {string} */
            sourceBodyCoverage: "unavailable";
            coverage: {
                /** @enum {string} */
                status: "complete" | "limited";
                returned: number;
                omittedEntries: number;
            };
            items: ({
                /** @enum {string} */
                kind: "reported_experience";
                record: {
                    id: string;
                    content: string;
                    kind: string;
                    epistemicKind?: string;
                    status: string;
                    learnedAt: string;
                    occurredAt?: string;
                    validFrom?: string;
                    validUntil?: string;
                    sources: {
                        sceneId: string;
                        messageId: string;
                        senderId: string;
                    }[];
                };
                time: {
                    occurredAt: string | null;
                    recordedAt: string;
                };
            } | {
                /** @enum {string} */
                kind: "shared_plan_declaration";
                view: {
                    sourceMessageIds: string[];
                    terms?: {
                        /** @enum {string} */
                        kind: "plan";
                        title: string;
                        participantIds: string[];
                        /** @enum {string} */
                        temporalScope: "current" | "past" | "future" | "unspecified";
                        /** @enum {string} */
                        polarity: "positive" | "negative";
                        /** @enum {string} */
                        modality: "asserted" | "possible";
                        conditions: string[];
                        timeQualification?: string;
                        /** Format: date-time */
                        occurredAt?: string;
                        /** Format: date-time */
                        validFrom?: string;
                        /** Format: date-time */
                        validUntil?: string;
                    };
                    recordedAt?: string;
                    /** @enum {string} */
                    confirmation: "proposed" | "confirmed" | "incomplete";
                    /** @enum {string} */
                    lifecycle: "pending" | "done" | "cancelled" | "needs_review";
                    /** @enum {string} */
                    qualification: "declared_not_verified";
                    /** @enum {string} */
                    sourceBodyCoverage: "unavailable";
                    confirmedParticipantIds: string[];
                    missingParticipantIds: string[];
                    completedParticipantIds: string[];
                    cancelledParticipantIds: string[];
                };
                /** @enum {string} */
                completionBasis: "participant_self_report_not_observed_event";
            })[];
        };
    };
    responses: never;
    parameters: never;
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export type operations = Record<string, never>;
