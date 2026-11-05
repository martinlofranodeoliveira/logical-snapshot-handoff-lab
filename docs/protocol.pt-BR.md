# Subconjunto de protocolo e inspeção

Referências oficiais:

- https://www.postgresql.org/docs/17/logicaldecoding-explanation.html
- https://www.postgresql.org/docs/17/protocol-logicalrep-message-formats.html
- https://github.com/jackc/pglogrepl
- https://github.com/jackc/pgx
- https://www.sqlite.org/atomiccommit.html

Transporte usa pgx/pgconn; comandos e decodificação de replicação usam pglogrepl. Não há parser de protocolo escrito à mão em runtime. Encoders sintéticos dos testes apenas constroem fixtures independentes.

Opções: proto_version=1, publication_names=lab_pub, binary=false, streaming=false, messages=false, two_phase=false.

| Mensagem | Regra |
|---|---|
| Begin | sem transação ativa; guarda FinalLSN |
| Relation | OID/nome/namespace exatos de items, identidade DEFAULT, três colunas com flags/OIDs/modificadores exatos |
| Insert | tupla textual completa de três colunas |
| Update | nova tupla textual completa; tupla antiga proibida |
| Delete | chave K com três posições, ID textual e demais posições NULL |
| Truncate | apenas items, sem opções CASCADE ou RESTART IDENTITY |
| Commit | transação ativa, flags zero, CommitLSN=Begin.FinalLSN e EndLSN maior |

Outras mensagens falham fechadas. Cada WALData real recebido do servidor próprio é entregue ao pglogrepl. O dicionário contém somente a relação admitida e reinicia vazio a cada conexão. Valores binários, NULL em valores obrigatórios, TOAST inalterado e texto int8 não canônico são rejeitados. Labels pequenos na fixture não precisam de placeholders TOAST.

A captura inclui a última Relation validada se ela precedeu Begin, além dos frames reais da transação. A resposta JSON contém arrays de bytes em base64. O controlador decodifica a captura em uma nova instância, aplica sobre imagem SQLite separada iniciada no baseline e reaplica a mesma transação, verificando linhas esperadas, checkpoint e contador. Não existe log persistente ilimitado.

Keepalive pode pedir feedback, mas não avança checkpoint. WAL final e timestamp do servidor não governam semântica. Só ponto consistente inicial e Commit.EndLSN podem avançar a posição. A barreira feedback permite matar o worker após commit durável e antes do reconhecimento. Na retomada, o confirmed point antigo permite repetir endpoint; isso incrementa apenas diagnóstico de duplicata, não o contador durável.

JSON de controle é síncrono, local e herdado. Não foi projetado/autenticado para exposição em rede.
