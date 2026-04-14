export type FAQItem = {
  question: string;
  answer: string;
};

export const coreFaqs: FAQItem[] = [
  {
    question: 'Can Grafana use VictoriaLogs with the native Loki datasource?',
    answer:
      "Yes. Loki-VL-proxy exposes a Loki-compatible read API in front of VictoriaLogs so Grafana can keep using its built-in Loki datasource while the backend is VictoriaLogs.",
  },
  {
    question: 'Do I need a custom plugin?',
    answer:
      'No. The project is specifically built to avoid a custom Grafana datasource plugin. Grafana talks to the proxy as if it were talking to Loki.',
  },
  {
    question: 'Does it support Explore and Logs Drilldown?',
    answer:
      'Yes. The project keeps explicit compatibility tracks for Grafana Explore, the Loki datasource, Logs Drilldown, and VictoriaLogs-backed behavior, including the Loki-compatible patterns endpoint used by Drilldown.',
  },
  {
    question: 'Is it read-only?',
    answer:
      'Yes. Loki-VL-proxy is intentionally a read/query proxy. Query, metadata, patterns, and rules or alerts read views are in scope. Push remains blocked.',
  },
  {
    question: 'Can Loki-VL-proxy plus VictoriaLogs be cheaper than Loki?',
    answer:
      "Often yes on search-heavy or repeated-read workloads, but the answer is workload-dependent. Grafana's own Loki sizing guide already reaches 431 vCPU / 857 Gi at 3-30 TB/day before query spikes, VictoriaLogs publishes lower-RAM and lower-disk claims plus all-field indexing, and the proxy adds cache layers that suppress repeated backend work. Validate the result with your own route-aware metrics and query mix.",
  },
  {
    question: 'Does Grafana automatically use zstd with the proxy?',
    answer:
      'Not today in the normal datasource-proxy path we verified against Grafana 12.4.2. The proxy now supports zstd on the hops it controls, but Grafana advertised `Accept-Encoding: deflate, gzip`, not `zstd`, upstream in our verification, so auto mode mainly benefits direct clients and peer-cache hops unless you force zstd explicitly.',
  },
];
