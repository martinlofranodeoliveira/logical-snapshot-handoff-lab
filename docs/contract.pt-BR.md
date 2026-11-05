# Contrato de desenvolvimento 0.1.0

Laboratório local para Linux; não é uma declaração de conformidade geral com PostgreSQL ou SQLite. Operações: `bootstrap`, `catchUp`, `status`, `stop`. Uma solicitação síncrona por vez, sem serviço de controle em rede.

## Recursos e dados

O controlador cria um único PG17 privado em `/tmp/lsh-*`, modo 0700, sob UID não root. O postmaster fica em primeiro plano e só aceita socket Unix no diretório privado. Não há endpoint configurável nem TCP. Variáveis `PG*` são removidas antes do parser pgx; subprocessos PostgreSQL recebem ambiente mínimo. O controlador possui todos os processos, pipes e arquivos. Shutdown do postmaster: SIGINT, espera de cinco segundos, kill do grupo próprio e mais cinco segundos para reap. Sem reap, o diretório é preservado e há erro. Workers também têm reap limitado a cinco segundos.

Relação fixa `public.items`, publicação `lab_pub`, slot `lab_slot`, plugin `pgoutput`, identidade DEFAULT. Ordem/OIDs/modificadores/NOT NULL das colunas e flags de chave são verificados. `id` é int64 com chave primária, `label` é texto UTF-8 sem NUL com até 256 bytes e `revision` é int64 positivo.

Bootstrap rejeita a linha 33. O estado final tem até 32 linhas. Cada transação tem até 64 operações e 65.536 bytes decodificados: insert/update = 8 bytes de ID + 8 de revisão + bytes do label; delete = 8; truncate = 0 bytes, mas conta uma operação. O limite de linhas considera o estado final, não os estados internos invisíveis.

Mensagem lógica: até 64 KiB; CopyData recebida: até 96 KiB; captura bruta de uma transação: até 128 KiB, incluindo metadados. Um único item no dicionário de relações. Apenas a captura mais recente fica na memória. Resposta: até 512 KiB; solicitação: 2 KiB; configuração: 4 KiB; logs capturados: 16 KiB; diagnóstico retornado: 600 bytes sanitizados. O leitor pgx pode alocar o frame antes da verificação de CopyData; a origem é o servidor local próprio, não um peer remoto hostil.

Execução sequencial com `GOMAXPROCS=2` e `GOMEMLIMIT=256MiB`; PostgreSQL usa shared_buffers 16 MiB, work_mem 1 MiB, maintenance_work_mem 16 MiB, oito conexões e um slot. São orçamentos de laboratório, não cotas agregadas impostas pelo sistema operacional. Retenção de WAL, autocheckpoint SQLite de 64 páginas e journal_size_limit de 1 MiB são orientações, nunca cotas físicas de disco. Leitores longos podem reter WAL.

## Bootstrap e replay

Bootstrap é sempre explícito. Verifica esquema/publicação/identidade, remove explicitamente o slot próprio existente e cria outro com `EXPORT_SNAPSHOT`. O exportador continua aberto, sem novos comandos, enquanto outra transação `REPEATABLE READ READ ONLY` executa `SET TRANSACTION SNAPSHOT` antes de consultar dados. SQLite publica atomicamente linhas, ponto consistente, system identifier, OID do banco/tabela, nomes de slot/publicação e contador zero. Snapshot inicial não conta como transação aplicada. Falhas preservam a projeção anterior, mas podem já ter substituído o slot; nesse caso outro bootstrap explícito pode ser necessário.

Protocolo v1 textual, sem streaming, mensagens lógicas ou two-phase. A transação inteira fica em buffer. Suporta insert, atualização sem mudança de chave, delete com chave DEFAULT e truncate de uma relação sem opções. UPDATE com tupla antiga é rejeitado. Metadados de relação ausentes não são adivinhados. Layout, tipo, mensagem, TOAST inalterado, binário ou cronologia inesperados falham fechados.

`Commit.CommitLSN` precisa coincidir com FinalLSN de Begin; `Commit.TransactionEndLSN`, maior, é o endpoint. LSN é uint64 persistido como TEXT hexadecimal minúsculo de 16 posições, nunca INTEGER assinado. Não há ordenação por relógio.

SQLite WAL/FULL confirma linhas, endpoint e contador escalar na mesma transação. Insert conflitante ou update/delete de chave ausente desfaz tudo. Endpoint <= checkpoint não altera linhas, checkpoint ou contador. Não há tabela histórica ilimitada. Overflow do contador falha.

Feedback só usa checkpoint confirmado em SQLite, nunca WALStart, WAL do servidor ou horário do keepalive. Erros de limite/layout/tipo não avançam checkpoint nem reconhecem a transação. Keepalive pode receber um checkpoint anterior já durável. A barreira `feedback` fica após commit e antes do reconhecimento. Morte do processo nesse ponto permite replay idempotente, não uma promessa de transporte exactly-once.

Um flock exclusivo acompanha toda a vida do escritor. Leitores independentes abrem SQLite somente leitura e usam transações de snapshot; podem continuar vendo a imagem antiga depois do commit do escritor.

## Retomada e controle

Antes do replay, compara-se identidade persistida com origem/publicação. Slot deve existir, usar pgoutput e banco lab, não estar invalidado nem com WAL perdido. confirmed_flush_lsn e restart_lsn precisam existir e não ultrapassar o checkpoint; checkpoint não pode ultrapassar WAL atual da origem. Violação retorna `REBOOTSTRAP_REQUIRED` preservando projeção. Isso não autentica adulterações locais nem cobre failover geral. Nunca se recria slot implicitamente.

A retomada parte do confirmed point do slot, permitindo repetir commit cujo feedback se perdeu. Dicionário de relações inicia vazio, reconstruído das mensagens reais.

`catchUp(alvo)` só tem sucesso com slot consistente e checkpoint durável >= alvo. Um `pg_current_wal_lsn()` arbitrário pode conter WAL não publicado e nunca ser atingido por um endpoint. Deadline/cancelamento retorna `INCOMPLETE`; toda resposta com erro marca incomplete=true. Fixtures pedem checkpoint anterior + 1 apenas depois de confirmar a próxima transação publicada conhecida, aprendendo o endpoint real na resposta. O teste de rollback pede incremento inalcançável e não introduz marcador.

Campos de solicitação: action, target (LSN hexadecimal PostgreSQL), barrier (vazio/snapshot/commit/feedback), timeout_ms (1..5000; zero usa 5000). Campos/operações/barreiras desconhecidos falham. `status` lê imagem SQLite completa e qualificação da biblioteca; não prova conectividade ou sincronização atual. `stop` responde e fecha o escritor; EOF também fecha. Formato inválido encerra o worker.

Códigos centrais: `REBOOTSTRAP_REQUIRED`, `TRANSACTION_LIMIT`, `RELATION_LAYOUT`, `PRIMARY_KEY_MUTATION`, `UNSUPPORTED_TYPE`, `UNSUPPORTED_MESSAGE`, `INVALID_PROTOCOL`, `INVALID_ROW`, `APPLY_INVALID`, `INCOMPLETE`, `WRITER_BUSY`, `SQLITE_QUALIFICATION`. Falhas inesperadas de I/O/biblioteca usam INTERNAL ou SOURCE_ERROR e diagnóstico limitado.

Saída 0 da demonstração significa oito verificações concluídas, inclusive falhas esperadas. O slot final está ausente intencionalmente e a origem final contém uma transação não admitida. O relatório do experimento não equivale a sucesso de catchUp. Não há afirmação de durabilidade contra perda de energia: isso depende do armazenamento.
