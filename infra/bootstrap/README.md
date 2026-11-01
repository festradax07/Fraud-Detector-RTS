# Bootstrap de AWS

Lo único permanente del proyecto: la **alarma de presupuesto** (gratis).
Se aplica **una sola vez**, desde la notebook. Todo lo demás es efímero y
vive en `infra/entornos/fase1`.

```sh
cd infra/bootstrap
terraform init
terraform plan  -var 'email_alertas=TU_MAIL'
terraform apply -var 'email_alertas=TU_MAIL'
```

Avisa por mail al 50% y al 80% del tope del mes (por defecto, $10), y si el
pronóstico del mes lo supera. Las alertas llegan por mail directo, sin
confirmar ninguna suscripción; si no aparecen, revisar el spam.

## El state es local

Terraform lo maneja una sola persona, así que el state queda en esta carpeta
(`terraform.tfstate`), sin bucket: no hay que compartirlo ni bloquearlo.

- **No va a git** (está en el `.gitignore`): puede tener datos sensibles.
- **Conviene un backup.** Si se pierde, la alarma sigue existiendo en AWS,
  pero Terraform se olvida de ella (se borra a mano desde la consola, en
  Billing → Budgets).
- Todo lo del proyecto lleva la etiqueta `proyecto=drts`, para encontrarlo
  aunque se pierda un state.
