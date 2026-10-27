# Runbook: ErrorBudgetBurnFast / ErrorBudgetBurnMedium / OrderEventsNotPublished

**SLO:** 99,9 % успешных API-запросов за 30 дней (бюджет ≈ 43 мин полного простоя).
Fast burn 14,4× съедает 2 % месячного бюджета за час — реагировать немедленно.

## 1. Был ли релиз?

```bash
helm -n shop history orders | tail -3
helm -n shop history catalog | tail -3
```

Релиз в последние 30 минут → откатить, потом разбираться:

```bash
helm -n shop rollback orders              # orders
./scripts/blue-green.sh rollback catalog  # catalog: мгновенно, старый цвет ещё прогрет
```

## 2. Где ошибки

```promql
sum by (route, code) (rate(http_requests_total{app="orders",code=~"5.."}[5m]))
```

```bash
kubectl -n shop logs -l app.kubernetes.io/name=orders --since=10m | jq -c 'select(.status>=500)' | head
```

| Картина | Вероятная причина | Действие |
|---|---|---|
| 5xx на всех маршрутах, `/readyz` = 503 по `postgres` | БД: failover, исчерпан пул | проверить `c-<id>.rw` в консоли MDB, соединения в PgBouncer |
| `OrderEventsNotPublished`, `/readyz` = 503 по `rabbitmq` | брокер недоступен / нет места | `rabbitmq-diagnostics status`, алармы по памяти/диску |
| рост p99, затем 5xx | достигнут `maxReplicas` HPA | `kubectl -n shop get hpa`; временно поднять `autoscaling.maxReplicas` |
| 5xx на части подов | проблема узла / зоны | `kubectl get pods -o wide`, cordon узла |

## 3. Коммуникация

- Fast burn дольше 15 минут → статус в канале инцидентов, назначить координатора.
- После восстановления — постмортем, если израсходовано > 20 % месячного бюджета.
